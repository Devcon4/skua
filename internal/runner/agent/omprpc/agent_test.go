package omprpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
	"github.com/Devcon4/skua/internal/runner/agent"
	"github.com/Devcon4/skua/internal/runner/agent/omprpc/omprpctest"
)

const (
	fakeOmpEnv         = "OMPRPC_FAKEOMP"
	fakeOmpLogEnv      = "OMPRPC_FAKEOMP_LOG"
	fakeOmpAgentDirEnv = "OMPRPC_FAKEOMP_AGENT_DIR"
	testKey            = "sk-skua-0123456789abcdef0123456789abcdef"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeOmpEnv) == "1" {
		os.Exit(runFakeOmp())
	}
	// Race-built children otherwise sleep 1 s at exit.
	_ = os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	os.Exit(m.Run())
}

func runFakeOmp() int {
	var in io.Reader = os.Stdin
	if dir := os.Getenv(fakeOmpAgentDirEnv); dir != "" {
		_ = os.Setenv(agentDirEnv, dir)
	}
	if path := os.Getenv(fakeOmpLogEnv); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 1
		}
		defer func() { _ = f.Close() }()
		in = io.TeeReader(os.Stdin, f)
	}
	return omprpctest.Main(os.Args[1:], in, os.Stdout)
}

type harness struct {
	t      *testing.T
	a      *Agent
	params agent.StartParams
	log    string

	mu     sync.Mutex
	events []wire.Event
	auth   map[string][]string
}

func newHarness(t *testing.T, models []string, args ...string) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, a: New(), log: filepath.Join(dir, "commands.jsonl"), auth: map[string][]string{}}
	llm := litellmtest.NewServer(nil, clock.RealClock{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.auth[r.URL.Path] = append(h.auth[r.URL.Path], r.Header.Get("Authorization"))
		h.mu.Unlock()
		llm.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	agentDir := filepath.Join(dir, "agent")
	h.params = agent.StartParams{
		Command: append([]string{os.Args[0], "--mode", "rpc"}, args...),
		Env: append(os.Environ(),
			fakeOmpEnv+"=1",
			fakeOmpLogEnv+"="+h.log,
		),
		WorkDir:        dir,
		AgentDir:       agentDir,
		SessionDir:     filepath.Join(dir, "session"),
		GatewayBaseURL: srv.URL,
		GatewayKey:     testKey,
		Models:         models,
		MCP: agent.MCPConfig{
			Command:    []string{"/skua/bin/skua-runner", "mcp"},
			SocketPath: "/skua/run/runner.sock",
			SchemaFile: "/skua/run/schema.json",
		},
	}
	go func() {
		for e := range h.a.Events() {
			h.mu.Lock()
			h.events = append(h.events, e)
			h.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.a.Stop(ctx)
	})
	return h
}

func (h *harness) start() error {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return h.a.Start(ctx, h.params)
}

func (h *harness) mustStart() {
	h.t.Helper()
	if err := h.start(); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
}

func (h *harness) prompt(text string) agent.TurnResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := h.a.Prompt(ctx, text)
	if err != nil {
		h.t.Fatalf("Prompt(%q): %v", text, err)
	}
	return r
}

func (h *harness) waitEvent(pred func(wire.Event) bool) wire.Event {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		i := slices.IndexFunc(h.events, pred)
		var e wire.Event
		if i >= 0 {
			e = h.events[i]
		}
		h.mu.Unlock()
		if i >= 0 {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.t.Fatalf("no matching event; events: %v", h.events)
	return wire.Event{}
}

func (h *harness) texts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		if e.Type == wire.EventText {
			var d wire.TextData
			_ = json.Unmarshal(e.Data, &d)
			out = append(out, d.Delta)
		}
	}
	return out
}

func (h *harness) commands(typ string) []map[string]any {
	h.t.Helper()
	b, err := os.ReadFile(h.log)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(b), "\n") {
		var c map[string]any
		if json.Unmarshal([]byte(line), &c) == nil && c["type"] == typ {
			out = append(out, c)
		}
	}
	return out
}

func isType(typ string) func(wire.Event) bool {
	return func(e wire.Event) bool { return e.Type == typ }
}

func requireReason(t *testing.T, err error, want string) {
	t.Helper()
	var e *agent.Error
	if !errors.As(err, &e) || e.Reason != want {
		t.Fatalf("error = %v, want reason %s", err, want)
	}
}

func TestSteerCoalescesWhileQueued(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"})
	h.mustStart()
	gate := filepath.Join(t.TempDir(), "gate")
	done := make(chan agent.TurnResult, 1)
	go func() {
		r, _ := h.a.Prompt(context.Background(), "SCRIPT:steps 3 100ms "+gate)
		done <- r
	}()
	h.waitEvent(isType(wire.EventToolStart))
	for _, s := range []string{"S1", "S2", "S3"} {
		if err := h.a.Steer(context.Background(), s); err != nil {
			t.Fatalf("Steer(%s): %v", s, err)
		}
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Status != "completed" {
			t.Fatalf("turn = %+v", r)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("prompt did not finish")
	}
	var got []string
	for _, c := range h.commands("steer") {
		got = append(got, c["message"].(string))
	}
	if want := []string{"S1", "S2\n\nS3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steers reaching omp = %q, want %q", got, want)
	}
	var received []string
	for _, s := range h.texts() {
		if strings.HasPrefix(s, "steer-received: ") {
			received = append(received, s)
		}
	}
	if want := []string{"steer-received: S1", "steer-received: S2\n\nS3"}; !reflect.DeepEqual(received, want) {
		t.Fatalf("steers delivered = %q, want %q", received, want)
	}
}

func TestPromptWaitsForSessionSettled(t *testing.T) {
	t.Parallel()
	const settleDelay = 200 * time.Millisecond
	h := newHarness(t, []string{"e2e-primary"}, "--settle-delay", settleDelay.String())
	h.mustStart()
	start := time.Now()
	r := h.prompt("SCRIPT:error 422 budget_exceeded")
	if elapsed := time.Since(start); elapsed < settleDelay {
		t.Fatalf("Prompt returned after %s, before session_settled at %s", elapsed, settleDelay)
	}
	if r.Status != "error" || r.Reason != v1alpha1.ReasonBudgetExceeded {
		t.Fatalf("turn = %+v", r)
	}
	if r := h.prompt("SCRIPT:reply after"); r.Status != "completed" {
		t.Fatalf("prompt after settle = %+v", r)
	}
}

func TestAbort(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"})
	h.mustStart()
	done := make(chan agent.TurnResult, 1)
	go func() {
		r, _ := h.a.Prompt(context.Background(), "SCRIPT:sleep 30")
		done <- r
	}()
	h.waitEvent(isType(wire.EventToolStart))
	if err := h.a.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Status != "aborted" || r.Reason != "" {
			t.Fatalf("turn = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("abort did not end the turn")
	}
}
