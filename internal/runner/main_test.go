package runner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
	"github.com/Devcon4/skua/internal/runner/agent/omprpc"
	"github.com/Devcon4/skua/internal/runner/gitcred"
)

type mainRun struct {
	*runnerEnv
	gateway string
	code    chan int
}

func startMain(t *testing.T) *mainRun {
	t.Helper()
	e := newRunnerEnv(t)
	llm := httptest.NewServer(litellmtest.NewServer(nil, clock.RealClock{}))
	t.Cleanup(llm.Close)
	t.Setenv(sandbox.EnvAPIURL, e.api.URL)
	t.Setenv(sandbox.EnvMaxResultBytes, "65536")
	t.Setenv(sandbox.EnvMaxRestarts, "2")
	e.api.MaxResultBytes(65536)
	m := &mainRun{runnerEnv: e, gateway: llm.URL, code: make(chan int, 1)}
	go func() {
		m.code <- MainLayout([]string{"--", filepath.Join(e.l.BinDir, "fakeomp"), "--mode", "rpc"}, e.l, omprpc.New())
	}()
	t.Cleanup(m.stop)
	return m
}

func (m *mainRun) assign(prompt, schema string, forge *wire.Forge) {
	m.t.Helper()
	a := wire.Assign{
		Run:     testRun,
		Task:    wire.Task{Prompt: prompt},
		Gateway: wire.Gateway{BaseURL: m.gateway, Key: "sk-skua-test", Models: []string{"e2e-primary"}},
		Forge:   forge,
	}
	if schema != "" {
		a.Task.Schema = json.RawMessage(schema)
	}
	m.send(wire.CommandAssign, a)
}

func (m *mainRun) texts() []string {
	var out []string
	for _, ev := range m.api.Events(testRun) {
		if ev.Type != wire.EventText {
			continue
		}
		var d wire.TextData
		if json.Unmarshal(ev.Data, &d) == nil {
			out = append(out, d.Delta)
		}
	}
	return out
}

func (m *mainRun) sawEvent(typ string) bool {
	return slices.ContainsFunc(m.api.Events(testRun), func(ev wire.RunnerEvent) bool { return ev.Type == typ })
}

func (m *mainRun) requireStepsOnce(n int) {
	m.t.Helper()
	texts := m.texts()
	for k := 1; k <= n; k++ {
		if c := countOf(texts, fmt.Sprintf("step %d", k)); c != 1 {
			m.t.Errorf("step %d seen %d times in %q", k, c, texts)
		}
	}
	evs := m.api.Events(testRun)
	for i, ev := range evs {
		if ev.Seq != int64(i)+1 {
			m.t.Fatalf("event %d has seq %d; seqs are not 1..%d", i, ev.Seq, len(evs))
		}
	}
}

func countOf(texts []string, s string) int {
	n := 0
	for _, x := range texts {
		if x == s {
			n++
		}
	}
	return n
}

func TestRunnerFakeOmpStepsWithSteers(t *testing.T) {
	m := startMain(t)
	gate := filepath.Join(t.TempDir(), "gate")
	m.assign("SCRIPT:steps 5 50ms "+gate, "", &wire.Forge{Token: "tok-1", ExpiresAt: time.Now().Add(time.Hour)})
	eventually(t, 20*time.Second, func() bool { return m.sawEvent(wire.EventToolStart) })
	m.send(wire.CommandSteer, wire.Steer{Message: "S1"})
	s2 := m.send(wire.CommandSteer, wire.Steer{Message: "S2"})
	eventually(t, 20*time.Second, func() bool { return m.savedState().LastCommand == s2 })
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	requireResult(t, m.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	m.requireStepsOnce(5)
	texts := m.texts()
	for _, want := range []string{"steer-received: S1", "steer-received: S2"} {
		if c := countOf(texts, want); c != 1 {
			t.Fatalf("%q seen %d times; texts %q", want, c, texts)
		}
	}
}

func TestRunnerFakeOmpSubmit(t *testing.T) {
	m := startMain(t)
	m.assign(`SCRIPT:submit {"title":"done"}`, testSchema, nil)
	res := m.result()
	requireResult(t, res, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if string(res.Result) != `{"title":"done"}` {
		t.Fatalf("result = %s", res.Result)
	}
}

func TestRunnerFakeOmpBudgetExceeded(t *testing.T) {
	m := startMain(t)
	m.assign("SCRIPT:error 422 budget_exceeded", "", nil)
	requireResult(t, m.result(), v1alpha1.PhaseFailed, v1alpha1.ReasonBudgetExceeded)
}

func TestRunnerFakeOmpStreamAndEventFaults(t *testing.T) {
	m := startMain(t)
	gate := filepath.Join(t.TempDir(), "gate")
	m.assign("SCRIPT:steps 6 10ms "+gate, "", nil)
	eventually(t, 20*time.Second, func() bool { return m.sawEvent(wire.EventToolStart) })
	m.api.FailEvents(http.StatusServiceUnavailable, 1)
	m.api.DropStreamAfter(1)
	s1 := m.send(wire.CommandSteer, wire.Steer{Message: "S1"})
	eventually(t, 20*time.Second, func() bool { return m.savedState().LastCommand == s1 })
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	requireResult(t, m.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	m.requireStepsOnce(6)
	if c := countOf(m.texts(), "steer-received: S1"); c != 1 {
		t.Fatalf("steer delivered %d times; texts %q", c, m.texts())
	}
	failed, resumed := 0, false
	for _, p := range m.api.EventPosts() {
		if p.Status == http.StatusServiceUnavailable {
			failed++
		}
	}
	for _, r := range m.api.Requests() {
		if r.Path == wire.RunnerStreamPath && r.LastEventID == "1" {
			resumed = true
		}
	}
	if failed != 1 || !resumed {
		t.Fatalf("failed event posts = %d, stream resumed after id 1 = %v", failed, resumed)
	}
}

func TestRunnerFakeOmpTokenCommand(t *testing.T) {
	m := startMain(t)
	gate := filepath.Join(t.TempDir(), "gate")
	m.assign("SCRIPT:steps 3 10ms "+gate, "", &wire.Forge{Token: "tok-1", ExpiresAt: time.Now().Add(time.Hour)})
	eventually(t, 20*time.Second, func() bool { return m.sawEvent(wire.EventToolStart) })
	if tok, err := gitcred.ReadToken(m.l.forgeTokenPath()); err != nil || tok.Token != "tok-1" {
		t.Fatalf("token file = %+v, %v", tok, err)
	}
	m.send(wire.CommandToken, wire.Token{Token: "tok-2", ExpiresAt: time.Now().Add(time.Hour)})
	eventually(t, 10*time.Second, func() bool {
		tok, err := gitcred.ReadToken(m.l.forgeTokenPath())
		return err == nil && tok.Token == "tok-2"
	})
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	requireResult(t, m.result(), v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
}

func (m *mainRun) stop() {
	select {
	case code := <-m.code:
		m.t.Errorf("Main returned %d before SIGTERM", code)
		return
	default:
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		m.t.Fatal(err)
	}
	select {
	case code := <-m.code:
		if code != exitTerminated {
			m.t.Errorf("Main exit code = %d, want %d", code, exitTerminated)
		}
	case <-time.After(30 * time.Second):
		m.t.Fatal("Main did not exit after SIGTERM")
	}
}
