package runner

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
)

func TestRunnerAgentExitRestartsWithResume(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.setLast("done")
	f.onPrompt = func(f *fakeAdapter, _ context.Context, n int, _ string) (agent.TurnResult, error) {
		if n == 0 {
			return f.crash()
		}
		return agent.TurnResult{Status: "completed"}, nil
	}
	e.start(f, e.settings())
	e.send(wire.CommandSteer, wire.Steer{Message: "ignored before assign"})
	e.send(wire.CommandAssign, plainAssign())
	requireResult(t, e.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	s := f.snapshot()
	if len(s.starts) != 2 || s.starts[0].Resume || !s.starts[1].Resume {
		t.Fatalf("starts = %d, resume flags %v", len(s.starts), []bool{s.starts[0].Resume, len(s.starts) > 1 && s.starts[1].Resume})
	}
	if !slices.Equal(s.prompts, []string{"do the task", resumePrompt}) {
		t.Fatalf("prompts = %q", s.prompts)
	}
	var st state
	b, err := os.ReadFile(e.l.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &st); err != nil || st.Restarts != 1 {
		t.Fatalf("state restarts = %d, %v", st.Restarts, err)
	}
}

func TestRunnerCrashLoop(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.onPrompt = func(f *fakeAdapter, _ context.Context, _ int, _ string) (agent.TurnResult, error) {
		return f.crash()
	}
	s := e.settings()
	s.maxRestarts = 2
	e.start(f, s)
	e.send(wire.CommandAssign, plainAssign())
	requireResult(t, e.result(), v1alpha1.PhaseFailed, v1alpha1.ReasonCrashLoop)
	if n := len(f.snapshot().starts); n != 3 {
		t.Fatalf("starts = %d, want 3", n)
	}
}

func (e *runnerEnv) restartWith(st state) {
	e.t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(e.l.RunDir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.l.statePath(), b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func TestRunnerResumesAfterContainerRestart(t *testing.T) {
	e := newRunnerEnv(t)
	a := plainAssign()
	e.restartWith(state{Assign: &a, Prepared: true, Prompted: true})
	f := newFakeAdapter()
	f.setLast("continued")
	e.start(f, e.settings())
	requireResult(t, e.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	s := f.snapshot()
	if len(s.starts) != 1 || !s.starts[0].Resume || !slices.Equal(s.prompts, []string{resumePrompt}) {
		t.Fatalf("starts=%d prompts=%q", len(s.starts), s.prompts)
	}
}

func TestRunnerRestartBeforeFirstPromptSendsTask(t *testing.T) {
	e := newRunnerEnv(t)
	a := plainAssign()
	e.restartWith(state{Assign: &a, Prepared: true})
	f := newFakeAdapter()
	f.setLast("done")
	e.start(f, e.settings())
	requireResult(t, e.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if s := f.snapshot(); len(s.starts) != 1 || !slices.Equal(s.prompts, []string{"do the task"}) {
		t.Fatalf("starts=%d prompts=%q", len(s.starts), s.prompts)
	}
	if !e.savedState().Prompted {
		t.Fatal("state not marked prompted after the task turn")
	}
}
