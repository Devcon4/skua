package runner

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/apiclient"
)

const (
	eventQuiet     = 100 * time.Millisecond
	eventSettleMax = 2 * time.Second
)

// startRelay detaches the relay from SIGTERM so terminate can drain the agent's last events into the outbox.
func (r *runner) startRelay() (stop func(deadline context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.relayEvents(ctx)
	}()
	return func(deadline context.Context) {
		cancel()
		select {
		case <-done:
		case <-deadline.Done():
		}
	}
}

func (r *runner) relayEvents(ctx context.Context) {
	events := r.ad.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			r.relaying.Store(true)
			r.client.Emit(e)
			r.lastRelay.Store(time.Now().UnixNano())
			r.relaying.Store(false)
		}
	}
}

// settleEvents waits until the adapter's queued events reached the outbox; the adapter has no drain signal,
// so a short quiet period stands in for one.
func (r *runner) settleEvents(ctx context.Context) {
	deadline := time.Now().Add(eventSettleMax)
	for time.Now().Before(deadline) {
		if !r.relaying.Load() && time.Since(time.Unix(0, r.lastRelay.Load())) >= eventQuiet {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(eventQuiet / 4):
		}
	}
}

func (r *runner) finish(ctx context.Context, res wire.RunnerResult) {
	r.update(func(st *state) { st.Result = &res })
	r.settleEvents(ctx)
	err := r.client.SubmitResult(ctx, res)
	if errors.Is(err, apiclient.ErrTooLarge) {
		slog.Error("skua-api refused the result as too large", "run", res.Run, "bytes", len(res.Result))
		res = wire.RunnerResult{Run: res.Run, Phase: string(v1alpha1.PhaseFailed), Reason: v1alpha1.ReasonResultTooLarge, Outputs: res.Outputs}
		r.update(func(st *state) { st.Result = &res })
		err = r.client.SubmitResult(ctx, res)
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		slog.Error("skua-api refused the result", "run", res.Run, "err", err)
	}
	r.killTree(ctx)
	r.update(func(st *state) { st.Finished = true })
	r.mu.Lock()
	r.driving = false
	r.mu.Unlock()
	slog.Info("run finished", "run", res.Run, "phase", res.Phase, "reason", res.Reason)
}
