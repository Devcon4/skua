package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/google/renameio/v2"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/Devcon4/skua/internal/contract/wire"
)

const (
	outboxName   = "outbox.jsonl"
	maxEventData = wire.MaxEventsBodyBytes - 4<<10
)

// record is one outbox line; a line without a type only carries the seq counter across compaction.
type record struct {
	Run  string          `json:"run,omitempty"`
	Seq  int64           `json:"seq"`
	Type string          `json:"type,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type entry struct {
	run  string
	ev   wire.RunnerEvent
	size int
}

// Emit assigns e the next seq and stores it in the outbox file and memory for the sender. It blocks while
// the unacked events hold the byte cap and never drops an event. Data over the per-event cap is replaced by
// a JSON string naming its size, so one event cannot exceed skua-api's events body cap.
func (c *Client) Emit(e wire.Event) {
	data := e.Data
	if len(data) > 0 && !json.Valid(data) {
		data, _ = json.Marshal(string(data))
	}
	if len(data) > maxEventData {
		c.opt.log.Warn("runner event data over the cap, replaced by a marker", "type", e.Type, "bytes", len(e.Data), "cap", maxEventData)
		data, _ = json.Marshal(fmt.Sprintf("skua: event data of %d bytes dropped, over the %d-byte cap", len(e.Data), maxEventData))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.unacked >= c.opt.maxUnacked && !c.closed {
		c.cond.Wait()
	}
	c.seq++
	ent := entry{run: c.run, ev: wire.RunnerEvent{Seq: c.seq, Type: e.Type, Data: data}}
	line, _ := marshal(record{Run: ent.run, Seq: ent.ev.Seq, Type: ent.ev.Type, Data: ent.ev.Data})
	line = append(line, '\n')
	ent.size = len(line)
	if c.file != nil {
		if _, err := c.file.Write(line); err != nil {
			c.opt.log.Error("runner outbox append failed, event kept in memory", "seq", ent.ev.Seq, "err", err)
		}
	}
	c.pending = append(c.pending, ent)
	c.unacked += ent.size
	if len(c.pending) >= c.opt.batchEvents || c.unacked >= c.opt.batchBytes {
		c.signal()
	}
}

// Close stops the sender and closes the outbox file. Unacked events stay in the state dir for the next client.
func (c *Client) Close() error {
	c.stop()
	<-c.sendDone
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.cond.Broadcast()
	if c.file == nil {
		return nil
	}
	err := c.file.Close()
	c.file = nil
	return err
}

func (c *Client) loadOutbox(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := os.ReadFile(c.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		var r record
		if len(line) == 0 || json.Unmarshal(line, &r) != nil {
			continue
		}
		c.seq = max(c.seq, r.Seq)
		if r.Run != "" {
			c.run = r.Run
		}
		if r.Type == "" {
			continue
		}
		size := len(line) + 1
		c.pending = append(c.pending, entry{run: r.Run, ev: wire.RunnerEvent{Seq: r.Seq, Type: r.Type, Data: r.Data}, size: size})
		c.unacked += size
	}
	return c.compactLocked()
}

func (c *Client) compactLocked() error {
	var b bytes.Buffer
	mark, _ := marshal(record{Run: c.run, Seq: c.seq})
	b.Write(mark)
	b.WriteByte('\n')
	for _, e := range c.pending {
		line, _ := marshal(record{Run: e.run, Seq: e.ev.Seq, Type: e.ev.Type, Data: e.ev.Data})
		b.Write(line)
		b.WriteByte('\n')
	}
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
	pf, err := renameio.NewPendingFile(c.path, renameio.WithStaticPermissions(0o600), renameio.WithTempDir(filepath.Dir(c.path)))
	if err != nil {
		return err
	}
	defer func() { _ = pf.Cleanup() }()
	if _, err := io.Copy(pf, &b); err != nil {
		return err
	}
	if err := pf.CloseAtomicallyReplace(); err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	c.file = f
	return nil
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) sendLoop(ctx context.Context) {
	defer close(c.sendDone)
	tick := time.NewTicker(c.opt.flushEvery)
	defer tick.Stop()
	backoff := c.opt.retryBackoff()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-c.wake:
		}
		if !c.flushPending(ctx, &backoff) {
			return
		}
	}
}

func (c *Client) flushPending(ctx context.Context, backoff *wait.Backoff) (ctxAlive bool) {
	for {
		run, evs := c.nextBatch()
		if evs == nil {
			return true
		}
		ack, err := c.postEvents(ctx, run, evs)
		if err == nil && !c.applyAck(run, ack) {
			err = fmt.Errorf("apiclient: ack %d is below seq %d", ack, evs[0].Seq)
		}
		if err == nil {
			*backoff = c.opt.retryBackoff()
			continue
		}
		if permanentRefusal(err) {
			c.opt.log.Error("skua-api refused runner events, dropping them", "run", run, "fromSeq", evs[0].Seq, "toSeq", evs[len(evs)-1].Seq, "err", err)
			c.applyAck(run, evs[len(evs)-1].Seq)
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		c.opt.log.Warn("runner events not acked, retrying", "run", run, "fromSeq", evs[0].Seq, "err", err)
		if !sleep(ctx, backoff.Step()) {
			return false
		}
	}
}

func (c *Client) nextBatch() (string, []wire.RunnerEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return "", nil
	}
	run := c.pending[0].run
	if run == "" {
		run = c.run
	}
	if run == "" {
		return "", nil
	}
	var evs []wire.RunnerEvent
	size := 0
	for i := range c.pending {
		e := &c.pending[i]
		if e.run == "" {
			e.run = run
		}
		if e.run != run || len(evs) >= c.opt.batchEvents || (len(evs) > 0 && size+e.size > c.opt.batchBytes) {
			break
		}
		evs = append(evs, e.ev)
		size += e.size
	}
	return run, evs
}

func (c *Client) applyAck(run string, ack int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.pending[:0]
	freed := 0
	for _, e := range c.pending {
		if e.run == run && e.ev.Seq <= ack {
			freed += e.size
			continue
		}
		kept = append(kept, e)
	}
	if freed == 0 {
		return false
	}
	clear(c.pending[len(kept):])
	c.pending = kept
	c.unacked -= freed
	if err := c.compactLocked(); err != nil {
		c.opt.log.Error("runner outbox compaction failed", "path", c.path, "err", err)
	}
	c.cond.Broadcast()
	if len(c.pending) == 0 {
		for _, ch := range c.drained {
			close(ch)
		}
		c.drained = nil
	}
	return true
}

func (c *Client) flush(ctx context.Context) error {
	c.mu.Lock()
	if len(c.pending) == 0 {
		c.mu.Unlock()
		return nil
	}
	if c.run == "" && slices.ContainsFunc(c.pending, func(e entry) bool { return e.run == "" }) {
		c.mu.Unlock()
		return ErrRunUnknown
	}
	ch := make(chan struct{})
	c.drained = append(c.drained, ch)
	c.mu.Unlock()
	c.signal()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
