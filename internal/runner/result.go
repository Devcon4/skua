package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/mcp"
)

const invalidResultPrompt = "You have not called submit_result with a valid result. Call it now."

// conclude turns the settled conversation into a result, re-prompting once for a missing schema result and
// once for an oversize result in a forge workspace.
func (r *runner) conclude(dctx context.Context, a wire.Assign, exited <-chan error) outcome {
	hasSchema := len(a.Task.Schema) > 0
	canCommit := a.Workspace != nil && a.Forge != nil
	schemaAsked, sizeAsked := false, false
	for {
		val, submitted := r.submission()
		if !submitted && hasSchema {
			if schemaAsked {
				return done(failed(a.Run, v1alpha1.ReasonResultInvalid))
			}
			schemaAsked = true
			if out, ok := r.turn(dctx, a.Run, invalidResultPrompt, exited); !ok {
				return out
			}
			continue
		}
		if !submitted {
			text, err := r.ad.LastText(dctx)
			if err != nil {
				return r.agentFailure(dctx, a.Run, err, exited)
			}
			if val, err = json.Marshal(text); err != nil {
				return done(failed(a.Run, v1alpha1.ReasonAgentError))
			}
		}
		if len(val) > r.s.maxResultBytes {
			if !canCommit || sizeAsked {
				return done(failed(a.Run, v1alpha1.ReasonResultTooLarge))
			}
			sizeAsked = true
			r.update(func(st *state) { st.Submission = nil })
			if out, ok := r.turn(dctx, a.Run, oversizePrompt(a.Run, len(val), r.s.maxResultBytes), exited); !ok {
				return out
			}
			continue
		}
		return done(wire.RunnerResult{Run: a.Run, Phase: string(v1alpha1.PhaseSucceeded), Reason: v1alpha1.ReasonCompleted, Result: val})
	}
}

func oversizePrompt(run string, size, limit int) string {
	return fmt.Sprintf("Your result is %d bytes, over the %d-byte result cap. Commit the full output to a file on branch skua/%s, "+
		"push it with `git push origin HEAD:skua/%s`, then call submit_result again with a short result that names the file path.",
		size, limit, run, run)
}

func (r *runner) submission() (json.RawMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st.Submission, r.st.Submission != nil
}

func (r *runner) acceptSubmissions(ln *mcp.Listener) {
	for {
		sub, err := ln.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		if r.st.Assign == nil || r.st.Result != nil || r.st.Finished {
			r.mu.Unlock()
			_ = sub.Reject("skua has no run waiting for a result")
			continue
		}
		r.st.Submission = sub.Value
		err = r.saveLocked()
		r.mu.Unlock()
		if err != nil {
			slog.Error("runner could not persist the submitted result", "err", err)
		}
		_ = sub.Ack()
	}
}
