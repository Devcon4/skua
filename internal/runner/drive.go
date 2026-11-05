package runner

import (
	"context"
	"log/slog"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func (r *runner) startDriver(ctx context.Context, resume bool) {
	dctx, cancel := context.WithCancel(ctx)
	driverDone := make(chan struct{})
	r.mu.Lock()
	r.driving, r.aborted, r.cancelDrive, r.driverDone = true, false, cancel, driverDone
	r.mu.Unlock()
	go func() {
		defer close(driverDone)
		defer cancel()
		r.drive(ctx, dctx, resume)
	}()
}

func (r *runner) drive(ctx, dctx context.Context, resume bool) {
	r.mu.Lock()
	a := *r.st.Assign
	pending := r.st.Result
	r.mu.Unlock()
	var res wire.RunnerResult
	if pending != nil {
		res = *pending
	} else {
		var ok bool
		if res, ok = r.execute(ctx, dctx, a, resume); !ok {
			return
		}
		res.Outputs = r.outputs(ctx, a)
	}
	r.finish(ctx, res)
}

type outcomeKind int

const (
	outcomeDone outcomeKind = iota
	outcomeCrashed
	outcomeCancelled
	outcomeStopped
)

type outcome struct {
	kind    outcomeKind
	result  wire.RunnerResult
	exitErr error
}

func done(res wire.RunnerResult) outcome { return outcome{kind: outcomeDone, result: res} }

func failed(run, reason string) wire.RunnerResult {
	return wire.RunnerResult{Run: run, Phase: string(v1alpha1.PhaseFailed), Reason: reason}
}

// execute runs the agent until the run has a result. ok is false when the runner is shutting down.
func (r *runner) execute(ctx, dctx context.Context, a wire.Assign, resume bool) (res wire.RunnerResult, ok bool) {
	r.mu.Lock()
	prepared := r.st.Prepared
	r.mu.Unlock()
	if !prepared {
		if err := r.prepare(dctx, a); err != nil {
			if out := r.interrupted(dctx); out != nil {
				return r.resolve(ctx, a, *out)
			}
			slog.Error("runner could not prepare the workspace", "run", a.Run, "err", err)
			return failed(a.Run, v1alpha1.ReasonWorkspaceError), true
		}
		r.update(func(st *state) { st.Prepared = true })
		resume = false
	}
	for {
		if resume {
			r.killTree(ctx)
		}
		out := r.attempt(dctx, a, resume)
		if out.kind != outcomeCrashed {
			return r.resolve(ctx, a, out)
		}
		var restarts int
		r.update(func(st *state) {
			st.Restarts++
			restarts = st.Restarts
		})
		slog.Warn("agent exited mid-run", "run", a.Run, "err", out.exitErr, "restarts", restarts, "maxRestarts", r.s.maxRestarts)
		if restarts > r.s.maxRestarts {
			r.killTree(ctx)
			return failed(a.Run, v1alpha1.ReasonCrashLoop), true
		}
		resume = true
	}
}

func (r *runner) resolve(ctx context.Context, a wire.Assign, out outcome) (wire.RunnerResult, bool) {
	switch out.kind {
	case outcomeCancelled:
		return r.cancel(ctx, a), true
	case outcomeStopped:
		return wire.RunnerResult{}, false
	}
	return out.result, true
}

// interrupted classifies a cancelled drive context: an abort cancels the run, anything else stops the runner.
func (r *runner) interrupted(dctx context.Context) *outcome {
	if dctx.Err() == nil {
		return nil
	}
	if r.isAborted() {
		return &outcome{kind: outcomeCancelled}
	}
	return &outcome{kind: outcomeStopped}
}

func (r *runner) cancel(ctx context.Context, a wire.Assign) wire.RunnerResult {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
	defer cancel()
	if err := r.ad.Abort(actx); err != nil {
		slog.Info("agent abort not delivered", "run", a.Run, "err", err)
	}
	if err := r.ad.Stop(actx); err != nil {
		slog.Info("agent stop failed", "run", a.Run, "err", err)
	}
	r.killTree(ctx)
	return failed(a.Run, v1alpha1.ReasonCancelled)
}
