package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
	"github.com/Devcon4/skua/internal/runner/agent/omprpc/omprpctest"
	"github.com/Devcon4/skua/internal/runner/apiclient"
	"github.com/Devcon4/skua/internal/runner/apiclient/apiclienttest"
	"github.com/Devcon4/skua/internal/runner/gitcred"
	"github.com/Devcon4/skua/internal/runner/mcp"
)

const (
	testRoleEnv = "SKUA_RUNNER_TEST_ROLE"
	testRun     = "fake-0123456789"
	testSchema  = `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`
	// Race-built children otherwise sleep 1 s at exit.
	raceFastExit = `GORACE="$GORACE atexit_sleep_ms=0"`
)

var testBinary string

func TestMain(m *testing.M) {
	switch os.Getenv(testRoleEnv) {
	case "fakeomp":
		os.Exit(omprpctest.Main(os.Args[1:], os.Stdin, os.Stdout))
	case "skua-runner":
		if len(os.Args) != 2 || os.Args[1] != "mcp" {
			os.Exit(2)
		}
		if err := mcp.Serve(context.Background(), os.Stdin, os.Stdout, os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "git-credential":
		os.Exit(gitcred.Helper(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, time.Now()))
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testBinary = exe
	os.Exit(m.Run())
}

type runnerEnv struct {
	t   *testing.T
	api *apiclienttest.Server
	l   Layout
}

func newRunnerEnv(t *testing.T) *runnerEnv {
	t.Helper()
	root := t.TempDir()
	e := &runnerEnv{
		t:   t,
		api: apiclienttest.New(t),
		l: Layout{
			BinDir:    filepath.Join(root, "bin"),
			RunDir:    filepath.Join(root, "run"),
			WorkDir:   filepath.Join(root, "work"),
			TokenPath: filepath.Join(root, "token"),
		},
	}
	if err := os.WriteFile(e.l.TokenPath, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.l.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]string{"fakeomp": "fakeomp", "skua-runner": "skua-runner", "git-credential-skua": "git-credential"} {
		script := "#!/bin/sh\n" + raceFastExit + " " + testRoleEnv + "=" + role + " exec '" + testBinary + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(e.l.BinDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *runnerEnv) settings() settings {
	return settings{
		maxResultBytes: sandbox.DefaultMaxResultBytes,
		maxRestarts:    sandbox.DefaultMaxRestarts,
		environ:        []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "KEEP=1"},
	}
}

func (e *runnerEnv) start(ad agent.Adapter, s settings) {
	e.t.Helper()
	stop := e.launch(ad, s)
	e.t.Cleanup(func() {
		if got := stop(); got != exitTerminated {
			e.t.Errorf("run exit code = %d, want %d", got, exitTerminated)
		}
	})
}

func (e *runnerEnv) launch(ad agent.Adapter, s settings) (stop func() int) {
	e.t.Helper()
	e.api.MaxResultBytes(s.maxResultBytes)
	client := apiclient.New(e.api.URL, e.l.TokenPath, e.l.RunDir, "0.1.0", nil)
	r := newRunner(e.l, s, []string{"agent"}, ad, client)
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- r.run(ctx) }()
	stop = sync.OnceValue(func() int {
		cancel()
		got := <-code
		_ = client.Close()
		return got
	})
	e.t.Cleanup(func() { stop() })
	return stop
}

func (e *runnerEnv) savedState() state {
	e.t.Helper()
	var st state
	b, err := os.ReadFile(e.l.statePath())
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (e *runnerEnv) streams() []apiclienttest.Request {
	var out []apiclienttest.Request
	for _, r := range e.api.Requests() {
		if r.Path == wire.RunnerStreamPath && r.Status == http.StatusOK {
			out = append(out, r)
		}
	}
	return out
}

func (e *runnerEnv) send(typ string, v any) int64 {
	e.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.api.Send(wire.Command{Type: typ, Data: b})
}

func (e *runnerEnv) result() wire.RunnerResult {
	e.t.Helper()
	eventually(e.t, 30*time.Second, func() bool {
		var st state
		b, err := os.ReadFile(e.l.statePath())
		return err == nil && json.Unmarshal(b, &st) == nil && st.Finished && len(e.api.Results()) > 0
	})
	return e.api.Results()[0].Result
}

func (e *runnerEnv) streamOpen() bool {
	return len(e.streams()) > 0
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func requireResult(t *testing.T, got wire.RunnerResult, phase v1alpha1.RunPhase, reason string) {
	t.Helper()
	if got.Run != testRun || got.Phase != string(phase) || got.Reason != reason {
		t.Fatalf("result = %s/%s/%s (%s), want %s/%s/%s", got.Run, got.Phase, got.Reason, got.Result, testRun, phase, reason)
	}
}

func plainAssign() wire.Assign {
	return wire.Assign{
		Run:     testRun,
		Task:    wire.Task{Prompt: "do the task"},
		Gateway: wire.Gateway{BaseURL: "http://gateway.invalid", Key: "sk-skua-test", Models: []string{"e2e-primary"}},
	}
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{events: make(chan wire.Event)}
}

func TestRunnerRestartResumesAfterLastAppliedCommand(t *testing.T) {
	e := newRunnerEnv(t)
	before := newFakeAdapter()
	before.onPrompt = func(_ *fakeAdapter, ctx context.Context, _ int, _ string) (agent.TurnResult, error) {
		<-ctx.Done()
		return agent.TurnResult{Status: "aborted"}, ctx.Err()
	}
	stop := e.launch(before, e.settings())
	e.send(wire.CommandAssign, plainAssign())
	eventually(t, 10*time.Second, func() bool { return len(before.snapshot().prompts) == 1 })
	e.send(wire.CommandSteer, wire.Steer{Message: "before restart"})
	eventually(t, 10*time.Second, func() bool { return e.savedState().LastCommand == 2 })
	if got := stop(); got != exitTerminated {
		t.Fatalf("first runner exit code = %d, want %d", got, exitTerminated)
	}
	if err := os.Remove(filepath.Join(e.l.RunDir, "outbox.jsonl")); err != nil {
		t.Fatal(err)
	}

	text, err := json.Marshal(wire.TextData{Delta: "after restart"})
	if err != nil {
		t.Fatal(err)
	}
	after := newFakeAdapter()
	after.setLast("continued")
	after.onPrompt = func(f *fakeAdapter, ctx context.Context, _ int, _ string) (agent.TurnResult, error) {
		select {
		case f.events <- wire.Event{Type: wire.EventText, Data: text}:
		case <-ctx.Done():
			return agent.TurnResult{Status: "aborted"}, ctx.Err()
		}
		for !slices.Contains(f.snapshot().steers, "after restart") {
			select {
			case <-ctx.Done():
				return agent.TurnResult{Status: "aborted"}, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
		return agent.TurnResult{Status: "completed"}, nil
	}
	first := len(e.streams())
	e.start(after, e.settings())
	eventually(t, 10*time.Second, func() bool { return len(after.snapshot().prompts) == 1 && len(e.streams()) > first })
	e.send(wire.CommandSteer, wire.Steer{Message: "after restart"})
	requireResult(t, e.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)

	if got := e.streams()[first].LastEventID; got != "2" {
		t.Fatalf("Last-Event-ID after restart = %q, want \"2\"", got)
	}
	if b, a := before.snapshot().steers, after.snapshot().steers; !slices.Equal(b, []string{"before restart"}) || !slices.Equal(a, []string{"after restart"}) {
		t.Fatalf("steers before restart = %q, after = %q", b, a)
	}
	if evs := e.api.Events(testRun); len(evs) != 1 || evs[0].Type != wire.EventText {
		t.Fatalf("events stored for %s = %+v, want the one text event", testRun, evs)
	}
	eventually(t, 10*time.Second, func() bool { return e.savedState().LastCommand == 3 })
}
