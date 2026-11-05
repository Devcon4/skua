package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Devcon4/skua/internal/contract/wire"
)

// Commands keeps one command stream open until ctx ends, reconnecting with backoff and resuming after the
// last command id it delivered or was given by SetLastEventID. The channel closes when ctx ends.
func (c *Client) Commands(ctx context.Context) <-chan wire.Command {
	c.mu.Lock()
	last := c.lastEventID
	c.mu.Unlock()
	out := make(chan wire.Command)
	go c.streamLoop(ctx, last, out)
	return out
}

func (c *Client) streamLoop(ctx context.Context, last int64, out chan<- wire.Command) {
	defer close(out)
	backoff := c.opt.retryBackoff()
	for {
		connected, err := c.streamOnce(ctx, &last, out)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = c.opt.retryBackoff()
		}
		c.opt.log.Warn("runner command stream ended, reconnecting", "lastEventID", last, "err", err)
		if !sleep(ctx, backoff.Step()) {
			return
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, last *int64, out chan<- wire.Command) (bool, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := c.newRequest(sctx, http.MethodGet, wire.RunnerStreamPath, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if *last > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(*last, 10))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, refusal(resp)
	}
	idle := time.AfterFunc(c.opt.streamIdle, cancel)
	defer idle.Stop()
	for m, err := range wire.ReadEvents(&idleReader{r: resp.Body, t: idle, d: c.opt.streamIdle}) {
		if err != nil {
			return true, err
		}
		id, err := strconv.ParseInt(m.ID, 10, 64)
		if err != nil {
			return true, fmt.Errorf("apiclient: command id %q: %w", m.ID, err)
		}
		if id <= *last {
			continue
		}
		cmd := wire.Command{ID: id, Type: m.Type, Data: m.Data}
		if cmd.Type == wire.CommandAssign {
			c.learnRun(cmd.Data)
		}
		select {
		case out <- cmd:
			*last = id
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
	return true, io.EOF
}

func (c *Client) learnRun(data []byte) {
	var a struct {
		Run string `json:"run"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return
	}
	c.SetRun(a.Run)
}

type idleReader struct {
	r io.Reader
	t *time.Timer
	d time.Duration
}

func (ir *idleReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 {
		ir.t.Reset(ir.d)
	}
	return n, err
}
