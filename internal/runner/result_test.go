package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
	"github.com/Devcon4/skua/internal/runner/mcp"
)

func TestRunnerLastTextResult(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.setLast("the answer")
	e.start(f, e.settings())
	e.send(wire.CommandAssign, plainAssign())
	res := e.result()
	requireResult(t, res, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if string(res.Result) != `"the answer"` {
		t.Fatalf("result = %s", res.Result)
	}
	if _, err := os.Stat(filepath.Join(e.l.repoDir(), skillDir, "SKILL.md")); err != nil {
		t.Fatalf("skill file: %v", err)
	}
}

func TestRunnerSchemaRepromptThenInvalid(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.setLast("no result submitted")
	e.start(f, e.settings())
	a := plainAssign()
	a.Task.Schema = json.RawMessage(testSchema)
	e.send(wire.CommandAssign, a)
	requireResult(t, e.result(), v1alpha1.PhaseFailed, v1alpha1.ReasonResultInvalid)
	s := f.snapshot()
	if !slices.Equal(s.prompts, []string{"do the task", invalidResultPrompt}) {
		t.Fatalf("prompts = %q", s.prompts)
	}
	if s.starts[0].MCP.SchemaFile != e.l.schemaPath() {
		t.Fatalf("schema file = %q", s.starts[0].MCP.SchemaFile)
	}
	if b, err := os.ReadFile(e.l.schemaPath()); err != nil || string(b) != testSchema {
		t.Fatalf("schema.json = %q, %v", b, err)
	}
}

func TestRunnerSubmissionIsResult(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.setLast("ignored text")
	f.onPrompt = func(f *fakeAdapter, _ context.Context, _ int, _ string) (agent.TurnResult, error) {
		if err := mcp.Submit(e.l.sockPath(), json.RawMessage(`{"title":"done"}`)); err != nil {
			return agent.TurnResult{}, err
		}
		return agent.TurnResult{Status: "completed"}, nil
	}
	e.start(f, e.settings())
	a := plainAssign()
	a.Task.Schema = json.RawMessage(testSchema)
	e.send(wire.CommandAssign, a)
	res := e.result()
	requireResult(t, res, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if string(res.Result) != `{"title":"done"}` {
		t.Fatalf("result = %s", res.Result)
	}
	if s := f.snapshot(); len(s.prompts) != 1 {
		t.Fatalf("prompts = %q", s.prompts)
	}
}

func TestRunnerOversizeWithoutWorkspace(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	f.setLast(strings.Repeat("x", 100))
	s := e.settings()
	s.maxResultBytes = 16
	e.start(f, s)
	e.send(wire.CommandAssign, plainAssign())
	res := e.result()
	requireResult(t, res, v1alpha1.PhaseFailed, v1alpha1.ReasonResultTooLarge)
	if len(f.snapshot().prompts) != 1 || (len(res.Result) != 0 && string(res.Result) != "null") {
		t.Fatalf("prompts = %q, result = %s", f.snapshot().prompts, res.Result)
	}
}

func TestRunnerSubmissionWithHTMLCharactersAtCap(t *testing.T) {
	e := newRunnerEnv(t)
	submitted := json.RawMessage(`{"title":"<a> & <b>"}`)
	f := newFakeAdapter()
	f.onPrompt = func(f *fakeAdapter, _ context.Context, _ int, _ string) (agent.TurnResult, error) {
		if err := mcp.Submit(e.l.sockPath(), submitted); err != nil {
			return agent.TurnResult{}, err
		}
		return agent.TurnResult{Status: "completed"}, nil
	}
	s := e.settings()
	s.maxResultBytes = len(submitted)
	e.start(f, s)
	a := plainAssign()
	a.Task.Schema = json.RawMessage(testSchema)
	e.send(wire.CommandAssign, a)
	res := e.result()
	requireResult(t, res, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if string(res.Result) != string(submitted) {
		t.Fatalf("result = %s, want %s", res.Result, submitted)
	}
}

func TestRunnerOversizeInWorkspaceReprompts(t *testing.T) {
	e := newRunnerEnv(t)
	forge := newTestForge(t)
	f := newFakeAdapter()
	f.onPrompt = func(f *fakeAdapter, _ context.Context, n int, text string) (agent.TurnResult, error) {
		switch n {
		case 0:
			f.setLast(strings.Repeat("x", 100))
		case 1:
			if !strings.Contains(text, "skua/"+testRun) || !strings.Contains(text, "submit_result") {
				return agent.TurnResult{}, fmt.Errorf("unexpected re-prompt %q", text)
			}
			f.setLast("see out.txt")
		}
		return agent.TurnResult{Status: "completed"}, nil
	}
	s := e.settings()
	s.maxResultBytes = 32
	s.forgeGitURL, s.forgeAPIURL = forge.gitURL, forge.api.URL
	e.start(f, s)
	e.send(wire.CommandAssign, workspaceAssign())
	res := e.result()
	requireResult(t, res, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if string(res.Result) != `"see out.txt"` || len(f.snapshot().prompts) != 2 {
		t.Fatalf("result = %s, prompts = %q", res.Result, f.snapshot().prompts)
	}
}
