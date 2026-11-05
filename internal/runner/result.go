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

type resultConclusion struct {
	r           *runner
	dctx        context.Context
	a           wire.Assign
	exited      <-chan error
	schemaAsked bool
	sizeAsked   bool
}

// conclude turns the settled conversation into a result, re-prompting once for a missing schema result and
// once for an oversize result in a forge workspace.
func (r *runner) conclude(dctx context.Context, a wire.Assign, exited <-chan error) outcome {
	c := &resultConclusion{r: r, dctx: dctx, a: a, exited: exited}
	for {
		if out, settled := c.next(); settled {
			return out
		}
	}
}

func (c *resultConclusion) next() (out outcome, settled bool) {
	val, submitted := c.r.submission()
	schemaResultMissing := !submitted && len(c.a.Task.Schema) > 0
	if schemaResultMissing {
		return c.repromptForSchema()
	}
	if !submitted {
		text, out, ok := c.lastTextResult()
		if !ok {
			return out, true
		}
		val = text
	}
	if len(val) > c.r.s.maxResultBytes {
		return c.repromptForSize(len(val))
	}
	return done(wire.RunnerResult{Run: c.a.Run, Phase: string(v1alpha1.PhaseSucceeded), Reason: v1alpha1.ReasonCompleted, Result: val}), true
}

func (c *resultConclusion) repromptForSchema() (outcome, bool) {
	if c.schemaAsked {
		return done(failed(c.a.Run, v1alpha1.ReasonResultInvalid)), true
	}
	c.schemaAsked = true
	return c.reprompt(invalidResultPrompt)
}

func (c *resultConclusion) repromptForSize(size int) (outcome, bool) {
	canCommit := c.a.Workspace != nil && c.a.Forge != nil
	if !canCommit || c.sizeAsked {
		return done(failed(c.a.Run, v1alpha1.ReasonResultTooLarge)), true
	}
	c.sizeAsked = true
	c.r.update(func(st *state) { st.Submission = nil })
	return c.reprompt(oversizePrompt(c.a.Run, size, c.r.s.maxResultBytes))
}

func (c *resultConclusion) reprompt(prompt string) (outcome, bool) {
	out, ok := c.r.turn(c.dctx, c.a.Run, prompt, c.exited)
	return out, !ok
}

func (c *resultConclusion) lastTextResult() (json.RawMessage, outcome, bool) {
	text, err := c.r.ad.LastText(c.dctx)
	if err != nil {
		return nil, c.r.agentFailure(c.dctx, c.a.Run, err, c.exited), false
	}
	val, err := json.Marshal(text)
	if err != nil {
		return nil, done(failed(c.a.Run, v1alpha1.ReasonAgentError)), false
	}
	return val, outcome{}, true
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
