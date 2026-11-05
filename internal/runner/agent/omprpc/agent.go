// Package omprpc is the omp-rpc adapter: it drives `omp --mode rpc` over stdio and
// relays its frames as run events.
package omprpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
	"github.com/Devcon4/skua/internal/runner/proc"
)

const (
	protocolVersion     = 2
	maxFrameBytes       = 1 << 20
	maxReassembledBytes = 64 << 20
	settleTimeout       = 60 * time.Second
	stopGrace           = 2 * time.Second
)

var (
	errNotStarted = errors.New("omprpc: agent not started")
	errRunning    = errors.New("omprpc: agent already running")
)

// Agent drives one `omp --mode rpc` process at a time and implements agent.Adapter.
// Start may be called again once the previous process has exited; Events spans every
// process, Exited belongs to the process of the latest Start.
type Agent struct {
	hc     *http.Client
	events *eventQueue

	mu        sync.Mutex
	p         *process
	steerBusy bool
	held      []string
}

var _ agent.Adapter = (*Agent)(nil)

func New() *Agent {
	return &Agent{hc: http.DefaultClient, events: newEventQueue()}
}

type response struct {
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

type promptResult struct {
	Status string `json:"status"`
	Error  *struct {
		Message    string `json:"message"`
		HTTPStatus int    `json:"httpStatus"`
	} `json:"error"`
	SessionSettled bool `json:"sessionSettled"`
}

func (a *Agent) current() *process {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.p
}

func (a *Agent) Start(ctx context.Context, sp agent.StartParams) error {
	if len(sp.Command) == 0 {
		return startFailed(errors.New("no agent command"))
	}
	if p := a.current(); p != nil && !p.ended() {
		return errRunning
	}
	models, err := writeConfig(ctx, a.hc, sp)
	if err != nil {
		return err
	}
	p, err := a.spawn(sp)
	if err != nil {
		return startFailed(err)
	}
	a.mu.Lock()
	a.p, a.steerBusy, a.held = p, false, nil
	a.mu.Unlock()
	if err := p.handshake(ctx, sp, models[0]); err != nil {
		_ = p.stdin.Close()
		return err
	}
	return nil
}

func (a *Agent) spawn(sp agent.StartParams) (*process, error) {
	cmd := exec.Command(sp.Command[0], sp.Command[1:]...)
	cmd.Dir = sp.WorkDir
	cmd.Env = append(slices.Clone(sp.Env), agentDirEnv+"="+sp.AgentDir, sandbox.EnvGatewayKey+"="+sp.GatewayKey)
	cmd.Stderr = os.Stderr
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout = inR, outW
	err = proc.Start(cmd)
	_ = inR.Close()
	_ = outW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		return nil, err
	}
	p := &process{
		a:          a,
		stdin:      inW,
		ready:      make(chan []byte, 1),
		settled:    make(chan struct{}, 1),
		readerDone: make(chan struct{}),
		procDone:   make(chan struct{}),
		exited:     make(chan error, 1),
		waiters:    map[string]chan []byte{},
	}
	go p.read(outR)
	go p.reap(cmd.Process.Pid)
	return p, nil
}

func (a *Agent) Prompt(ctx context.Context, text string) (agent.TurnResult, error) {
	p := a.current()
	if p == nil {
		return agent.TurnResult{}, errNotStarted
	}
	select {
	case <-p.settled:
	default:
	}
	id := p.nextID()
	resultKey := "prompt_result:" + id
	resultCh := p.expect(resultKey)
	resp, err := p.callID(ctx, id, map[string]any{"type": "prompt", "message": text})
	if err != nil {
		p.forget(resultKey)
		return agent.TurnResult{}, err
	}
	if !resp.Success {
		p.forget(resultKey)
		return agent.TurnResult{Status: "error", Reason: v1alpha1.ReasonAgentError, Message: resp.Error}, nil
	}
	raw, err := p.wait(ctx, resultCh)
	if err != nil {
		return agent.TurnResult{}, err
	}
	var r promptResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return agent.TurnResult{}, fmt.Errorf("omprpc: decode prompt_result: %w", err)
	}
	if !r.SessionSettled {
		t := time.NewTimer(settleTimeout)
		select {
		case <-p.settled:
		case <-t.C:
		case <-p.readerDone:
		case <-p.procDone:
		case <-ctx.Done():
			t.Stop()
			return agent.TurnResult{}, ctx.Err()
		}
		t.Stop()
	}
	return turnResult(r), nil
}

// Steer sends text now when omp's steering queue is empty; otherwise it holds text
// and sends every held text as one steer once the queue empties.
func (a *Agent) Steer(ctx context.Context, text string) error {
	a.mu.Lock()
	p := a.p
	if p == nil {
		a.mu.Unlock()
		return errNotStarted
	}
	if a.steerBusy {
		a.held = append(a.held, text)
		a.mu.Unlock()
		return nil
	}
	a.steerBusy = true
	a.mu.Unlock()
	resp, err := p.call(ctx, map[string]any{"type": "steer", "message": text})
	if err != nil {
		return err
	}
	if !resp.Success {
		a.mu.Lock()
		a.steerBusy = false
		a.mu.Unlock()
		return fmt.Errorf("omprpc: steer refused: %s", resp.Error)
	}
	return nil
}

func (a *Agent) queueUpdate(p *process, raw []byte) {
	var q struct {
		Steering []string `json:"steering"`
	}
	if json.Unmarshal(raw, &q) != nil {
		return
	}
	a.mu.Lock()
	if a.p != p {
		a.mu.Unlock()
		return
	}
	a.steerBusy = len(q.Steering) > 0
	var text string
	if !a.steerBusy && len(a.held) > 0 {
		text = strings.Join(a.held, "\n\n")
		a.held, a.steerBusy = nil, true
	}
	a.mu.Unlock()
	if text != "" {
		go func() { _ = p.send(p.nextID(), map[string]any{"type": "steer", "message": text}) }()
	}
}

func (a *Agent) Abort(ctx context.Context) error {
	a.mu.Lock()
	p := a.p
	a.held = nil
	a.mu.Unlock()
	if p == nil {
		return errNotStarted
	}
	resp, err := p.call(ctx, map[string]any{"type": "abort"})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("omprpc: abort refused: %s", resp.Error)
	}
	return nil
}

func (a *Agent) LastText(ctx context.Context) (string, error) {
	p := a.current()
	if p == nil {
		return "", errNotStarted
	}
	resp, err := p.call(ctx, map[string]any{"type": "get_last_assistant_text"})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("omprpc: get_last_assistant_text: %s", resp.Error)
	}
	var d struct {
		Text string `json:"text"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			return "", fmt.Errorf("omprpc: decode last assistant text: %w", err)
		}
	}
	return d.Text, nil
}

func (a *Agent) Events() <-chan wire.Event { return a.events.out }

func (a *Agent) Exited() <-chan error {
	if p := a.current(); p != nil {
		return p.exited
	}
	return nil
}

func (a *Agent) Drain(ctx context.Context) error {
	p := a.current()
	if p == nil {
		return nil
	}
	select {
	case <-p.readerDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	return a.events.drain(ctx)
}

// Stop closes omp's stdin and waits up to two seconds for it to exit; killing what
// remains is the caller's job.
func (a *Agent) Stop(ctx context.Context) error {
	p := a.current()
	if p == nil {
		return nil
	}
	_ = p.stdin.Close()
	t := time.NewTimer(stopGrace)
	defer t.Stop()
	select {
	case <-p.procDone:
	case <-t.C:
	case <-ctx.Done():
	}
	return nil
}
