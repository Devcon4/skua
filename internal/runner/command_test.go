package runner

import (
	"context"
	"testing"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
)

func TestRunnerWaitsForAssign(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.setLast("done")
	e.start(f, e.settings())
	eventually(t, 10*time.Second, e.streamOpen)
	e.send(wire.CommandSteer, wire.Steer{Message: "early"})
	abort := e.send(wire.CommandAbort, wire.Abort{})
	eventually(t, 10*time.Second, func() bool { return e.savedState().LastCommand == abort })
	if s := f.snapshot(); len(s.starts) != 0 || len(e.api.Results()) != 0 {
		t.Fatalf("before assign: %d starts, %d results", len(s.starts), len(e.api.Results()))
	}
	e.send(wire.CommandAssign, plainAssign())
	requireResult(t, e.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if s := f.snapshot(); len(s.starts) != 1 || len(s.steers) != 0 || s.aborts != 0 {
		t.Fatalf("starts=%d steers=%v aborts=%d", len(s.starts), s.steers, s.aborts)
	}
}

func TestRunnerRepeatedAssignIgnored(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.setLast("done")
	e.start(f, e.settings())
	e.send(wire.CommandAssign, plainAssign())
	e.send(wire.CommandAssign, plainAssign())
	e.result()
	last := e.send(wire.CommandAssign, plainAssign())
	eventually(t, 10*time.Second, func() bool { return e.savedState().LastCommand == last })
	if s := f.snapshot(); len(s.starts) != 1 || len(s.prompts) != 1 || len(e.api.Results()) != 1 {
		t.Fatalf("starts=%d prompts=%d results=%d", len(s.starts), len(s.prompts), len(e.api.Results()))
	}
}

func TestRunnerAbort(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.onPrompt = func(_ *fakeAdapter, ctx context.Context, _ int, _ string) (agent.TurnResult, error) {
		<-ctx.Done()
		return agent.TurnResult{Status: "aborted"}, ctx.Err()
	}
	e.start(f, e.settings())
	e.send(wire.CommandAssign, plainAssign())
	eventually(t, 10*time.Second, func() bool { return len(f.snapshot().prompts) == 1 })
	e.send(wire.CommandAbort, wire.Abort{})
	requireResult(t, e.result(), v1alpha1.PhaseFailed, v1alpha1.ReasonCancelled)
	if s := f.snapshot(); s.aborts != 1 || s.stops != 1 || len(s.starts) != 1 {
		t.Fatalf("aborts=%d stops=%d starts=%d", s.aborts, s.stops, len(s.starts))
	}
}
