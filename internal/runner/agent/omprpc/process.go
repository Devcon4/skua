package omprpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/runner/agent"
	"github.com/Devcon4/skua/internal/runner/proc"
)

type process struct {
	a     *Agent
	stdin *os.File

	writeMu sync.Mutex

	ready      chan []byte
	settled    chan struct{}
	readerDone chan struct{}
	procDone   chan struct{}
	exited     chan error

	mu      sync.Mutex
	ids     int64
	waiters map[string]chan []byte
	readErr error
	exitErr error
}

func (p *process) nextID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids++
	return "skua-" + strconv.FormatInt(p.ids, 10)
}

func (p *process) ended() bool {
	select {
	case <-p.procDone:
		return true
	default:
		return false
	}
}

func (p *process) expect(key string) chan []byte {
	ch := make(chan []byte, 1)
	p.mu.Lock()
	p.waiters[key] = ch
	p.mu.Unlock()
	return ch
}

func (p *process) forget(key string) {
	p.mu.Lock()
	delete(p.waiters, key)
	p.mu.Unlock()
}

func (p *process) deliver(key string, raw []byte) {
	p.mu.Lock()
	ch := p.waiters[key]
	delete(p.waiters, key)
	p.mu.Unlock()
	if ch != nil {
		ch <- bytes.Clone(raw)
	}
}

func (p *process) send(id string, cmd map[string]any) error {
	cmd["id"] = id
	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("omprpc: write %v: %w", cmd["type"], err)
	}
	return nil
}

func (p *process) call(ctx context.Context, cmd map[string]any) (response, error) {
	return p.callID(ctx, p.nextID(), cmd)
}

func (p *process) callID(ctx context.Context, id string, cmd map[string]any) (response, error) {
	key := "response:" + id
	ch := p.expect(key)
	if err := p.send(id, cmd); err != nil {
		p.forget(key)
		return response{}, err
	}
	raw, err := p.wait(ctx, ch)
	if err != nil {
		p.forget(key)
		return response{}, err
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return response{}, fmt.Errorf("omprpc: decode %v response: %w", cmd["type"], err)
	}
	return r, nil
}

func (p *process) wait(ctx context.Context, ch chan []byte) ([]byte, error) {
	select {
	case b := <-ch:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.readerDone:
	case <-p.procDone:
	}
	select {
	case b := <-ch:
		return b, nil
	default:
	}
	return nil, p.endErr()
}

func (p *process) endErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.readErr != nil:
		return fmt.Errorf("omprpc: reading agent output: %w", p.readErr)
	case p.exitErr != nil:
		return fmt.Errorf("omprpc: %w", p.exitErr)
	}
	return errors.New("omprpc: agent output closed")
}

func (p *process) handshake(ctx context.Context, sp agent.StartParams, model string) error {
	var raw []byte
	select {
	case raw = <-p.ready:
	case <-ctx.Done():
		return &agent.Error{Reason: v1alpha1.ReasonAgentStartFailed, Message: "no ready frame: " + ctx.Err().Error()}
	case <-p.readerDone:
	case <-p.procDone:
	}
	if raw == nil {
		select {
		case raw = <-p.ready:
		default:
			return &agent.Error{Reason: v1alpha1.ReasonAgentStartFailed, Message: "agent ended before ready: " + p.endErr().Error()}
		}
	}
	var ready struct {
		SupportedProtocolVersions []int `json:"supportedProtocolVersions"`
	}
	if err := json.Unmarshal(raw, &ready); err != nil {
		return startFailed(fmt.Errorf("decode ready: %w", err))
	}
	if !slices.Contains(ready.SupportedProtocolVersions, protocolVersion) {
		return &agent.Error{Reason: v1alpha1.ReasonAgentProtocolUnsupported, Message: fmt.Sprintf("agent offers RPC protocol versions %v, need %d", ready.SupportedProtocolVersions, protocolVersion)}
	}
	resp, err := p.call(ctx, map[string]any{"type": "negotiate_protocol", "protocolVersion": protocolVersion})
	if err != nil {
		return startFailed(err)
	}
	if !resp.Success {
		return &agent.Error{Reason: v1alpha1.ReasonAgentProtocolUnsupported, Message: resp.Error}
	}
	open := map[string]any{"type": "open_session", "sessionDir": sp.SessionDir}
	if !sp.Resume {
		open["provider"], open["modelId"], _ = strings.Cut(model, "/")
	}
	if resp, err = p.call(ctx, open); err != nil {
		return startFailed(err)
	}
	if !resp.Success {
		if strings.Contains(resp.Error, "Model not found") {
			return &agent.Error{Reason: v1alpha1.ReasonModelNotFound, Message: resp.Error}
		}
		return &agent.Error{Reason: v1alpha1.ReasonAgentStartFailed, Message: "open_session: " + resp.Error}
	}
	if resp, err = p.call(ctx, map[string]any{"type": "set_subagent_subscription", "level": "events"}); err != nil {
		return startFailed(err)
	}
	if !resp.Success {
		return &agent.Error{Reason: v1alpha1.ReasonAgentStartFailed, Message: "set_subagent_subscription: " + resp.Error}
	}
	return nil
}

func (p *process) reap(pid int) {
	ws := proc.StartReaper().Wait(pid)
	var err error
	switch {
	case ws.Signaled():
		err = fmt.Errorf("agent killed by %s", ws.Signal())
	case ws.ExitStatus() != 0:
		err = fmt.Errorf("agent exited with status %d", ws.ExitStatus())
	}
	p.mu.Lock()
	p.exitErr = err
	if err == nil {
		p.exitErr = errors.New("agent exited")
	}
	p.mu.Unlock()
	close(p.procDone)
	p.exited <- err
	close(p.exited)
}

func (p *process) read(out *os.File) {
	defer close(p.readerDone)
	defer func() { _ = out.Close() }()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrameBytes)
	var asm chunkAssembly
	for sc.Scan() {
		line := sc.Bytes()
		var h struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &h) != nil {
			continue
		}
		if h.Type == "rpc_chunk" {
			frame, err := asm.add(line)
			if err != nil {
				p.setReadErr(err)
				return
			}
			if frame == nil {
				continue
			}
			line = frame
			h.Type, h.ID = "", ""
			if err := json.Unmarshal(line, &h); err != nil {
				p.setReadErr(fmt.Errorf("decode reassembled frame: %w", err))
				return
			}
		}
		p.dispatch(h.Type, h.ID, line)
	}
	p.setReadErr(sc.Err())
}

func (p *process) setReadErr(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	p.readErr = err
	p.mu.Unlock()
}

func (p *process) dispatch(typ, id string, raw []byte) {
	switch typ {
	case "ready":
		select {
		case p.ready <- bytes.Clone(raw):
		default:
		}
	case "response":
		p.deliver("response:"+id, raw)
	case "prompt_result":
		p.deliver("prompt_result:"+id, raw)
	case "session_settled":
		select {
		case p.settled <- struct{}{}:
		default:
		}
	case "queue_update":
		p.a.queueUpdate(p, raw)
	default:
		if e, ok := mapEvent(raw); ok {
			p.a.events.push(e)
		}
	}
}
