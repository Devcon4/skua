package runner

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
)

const (
	exitWait     = 2 * time.Second
	resumePrompt = "The previous turn was interrupted because the agent process restarted. Check the workspace, then continue the task."
)

func (r *runner) attempt(dctx context.Context, a wire.Assign, resume bool) outcome {
	if err := r.ad.Start(dctx, r.startParams(a, resume)); err != nil {
		if out := r.interrupted(dctx); out != nil {
			return *out
		}
		slog.Error("agent did not start", "run", a.Run, "resume", resume, "err", err)
		return done(failed(a.Run, reasonOf(err, v1alpha1.ReasonAgentStartFailed)))
	}
	exited := r.ad.Exited()
	r.agentUp(dctx)
	defer r.agentDown()
	prompt := resumePrompt
	r.update(func(st *state) {
		if !st.Prompted {
			prompt = a.Task.Prompt
		}
		st.Prompted = true
	})
	if out, ok := r.turn(dctx, a.Run, prompt, exited); !ok {
		return out
	}
	return r.conclude(dctx, a, exited)
}

func (r *runner) agentUp(ctx context.Context) {
	r.mu.Lock()
	r.agentRunning = true
	held := r.steers
	r.steers = nil
	r.mu.Unlock()
	for _, msg := range held {
		r.sendSteer(ctx, msg)
	}
}

func (r *runner) agentDown() {
	r.mu.Lock()
	r.agentRunning = false
	r.mu.Unlock()
}

// turn sends one prompt and waits for it to settle; ok is false when the outcome ends the attempt.
func (r *runner) turn(dctx context.Context, run, text string, exited <-chan error) (out outcome, ok bool) {
	type reply struct {
		tr  agent.TurnResult
		err error
	}
	ch := make(chan reply, 1)
	go func() {
		tr, err := r.ad.Prompt(dctx, text)
		ch <- reply{tr, err}
	}()
	select {
	case rep := <-ch:
		if rep.err != nil {
			return r.agentFailure(dctx, run, rep.err, exited), false
		}
		if o := r.interrupted(dctx); o != nil {
			return *o, false
		}
		switch {
		case rep.tr.Reason != "":
			slog.Error("agent turn failed", "run", run, "reason", rep.tr.Reason, "message", rep.tr.Message)
			return done(failed(run, rep.tr.Reason)), false
		case rep.tr.Status != "completed":
			slog.Error("agent turn ended unexpectedly", "run", run, "status", rep.tr.Status, "message", rep.tr.Message)
			return done(failed(run, v1alpha1.ReasonAgentError)), false
		}
		return outcome{}, true
	case err := <-exited:
		return outcome{kind: outcomeCrashed, exitErr: err}, false
	case <-dctx.Done():
		return *r.interrupted(dctx), false
	}
}

func (r *runner) agentFailure(dctx context.Context, run string, err error, exited <-chan error) outcome {
	if out := r.interrupted(dctx); out != nil {
		return *out
	}
	if gone, exitErr := agentGone(exited); gone {
		return outcome{kind: outcomeCrashed, exitErr: exitErr}
	}
	slog.Error("agent call failed", "run", run, "err", err)
	return done(failed(run, reasonOf(err, v1alpha1.ReasonAgentError)))
}

func agentGone(exited <-chan error) (bool, error) {
	if exited == nil {
		return false, nil
	}
	t := time.NewTimer(exitWait)
	defer t.Stop()
	select {
	case err := <-exited:
		return true, err
	case <-t.C:
		return false, nil
	}
}

func reasonOf(err error, fallback string) string {
	var e *agent.Error
	if errors.As(err, &e) && e.Reason != "" {
		return e.Reason
	}
	return fallback
}
