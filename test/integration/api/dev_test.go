package api_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/auth"
	"github.com/Devcon4/skua/internal/api/auth/authtest"
	"github.com/Devcon4/skua/internal/api/dev"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

const devTestAudience = "skua-omp"

func TestDev(t *testing.T) {
	bootAPIEnv()
	t.Parallel()
	restCfg, c, cfg := startAPIEnv(t)
	issuer := authtest.New(t)
	cfg.OIDC.IssuerURL = issuer.URL()
	devTestProfile(t, c, "ready", true)
	devTestProfile(t, c, "notready", false)

	de := &devEnv{restCfg: restCfg, c: c, cfg: cfg, issuer: issuer}

	t.Run("authentication", func(t *testing.T) { testDevAuthentication(t, de) })

	t.Run("profiles", func(t *testing.T) { testDevProfiles(t, de) })

	t.Run("create", func(t *testing.T) { testDevCreate(t, de) })

	t.Run("ownership", func(t *testing.T) { testDevOwnership(t, de) })

	t.Run("events resume and end", func(t *testing.T) { testDevEventsResumeAndEnd(t, de) })

	t.Run("events heartbeat", func(t *testing.T) { testDevEventsHeartbeat(t, de) })

	t.Run("events for a terminal run without log", func(t *testing.T) { testDevEventsTerminalWithoutLog(t, de) })

	t.Run("fetched-at", func(t *testing.T) { testDevFetchedAt(t, de) })

	t.Run("steer", func(t *testing.T) { testDevSteer(t, de) })

	t.Run("abort without runner", func(t *testing.T) { testDevAbortWithoutRunner(t, de) })

	t.Run("abort with runner", func(t *testing.T) { testDevAbortWithRunner(t, de) })
}

func testDevAuthentication(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	for name, token := range map[string]string{
		"missing":     "",
		"wrong aud":   de.issuer.Token("alice", "someone-else"),
		"wrong iss":   de.issuer.Token("alice", devTestAudience, authtest.OtherIssuer("https://other.example")),
		"other key":   de.issuer.Token("alice", devTestAudience, authtest.OtherKey()),
		"expired":     de.issuer.Token("alice", devTestAudience, authtest.Expired()),
		"not a token": "garbage",
	} {
		if code, body := f.do("GET", "/v1alpha1/profiles", token, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("%s: status %d (%s), want 401", name, code, body)
		}
	}
	if code, body := f.do("GET", "/v1alpha1/profiles", f.token("alice"), nil, nil); code != http.StatusOK {
		t.Fatalf("valid token: status %d (%s), want 200", code, body)
	}
	de.issuer.RotateKey()
	if code, body := f.do("GET", "/v1alpha1/profiles", f.token("alice"), nil, nil); code != http.StatusOK {
		t.Fatalf("token after RotateKey: status %d (%s), want 200", code, body)
	}
}

func testDevProfiles(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	code, body := f.do("GET", "/v1alpha1/profiles", f.token("alice"), nil, nil)
	if code != http.StatusOK {
		t.Fatalf("status %d (%s)", code, body)
	}
	var got []wire.Profile
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	byName := map[string]wire.Profile{}
	for _, p := range got {
		byName[p.Name] = p
	}
	if p := byName["ready"]; !p.Ready || len(p.Models) != 1 || p.Models[0] != "model-a" || p.Description != "profile ready" {
		t.Errorf("ready profile = %+v", p)
	}
	if p, ok := byName["notready"]; !ok || p.Ready {
		t.Errorf("notready profile = %+v, present %v", p, ok)
	}
}

func testDevCreate(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	alice := f.token("alice")
	req := devTestCreate("ready", "create-1")
	req.Workspace = &wire.Workspace{Repo: "acme/widgets", BaseRef: "main"}
	req.Task.Schema = json.RawMessage(`{"type":"object"}`)
	first := f.createRun(alice, req, http.StatusCreated)
	owner := auth.Owner(de.issuer.URL(), "alice")
	if want := dev.RunName("ready", owner, "create-1"); first.Name != want || first.Phase != "Pending" {
		t.Fatalf("created %+v, want name %s phase Pending", first, want)
	}
	run := f.getRunObject(first.Name)
	if run.Labels[v1alpha1.LabelOwner] != owner || run.Labels[sandbox.LabelProfile] != "ready" {
		t.Errorf("labels = %v", run.Labels)
	}
	if run.Spec.Workspace == nil || run.Spec.Workspace.Repo != "acme/widgets" || run.Spec.Task.Schema == nil {
		t.Errorf("spec = %+v", run.Spec)
	}

	if again := f.createRun(alice, req, http.StatusOK); again.Name != first.Name {
		t.Errorf("retry returned %s, want %s", again.Name, first.Name)
	}
	if bobs := f.createRun(f.token("bob"), req, http.StatusCreated); bobs.Name == first.Name {
		t.Errorf("another owner got the same run %s", bobs.Name)
	}

	if code, body := f.do("POST", "/v1alpha1/runs", alice, devTestCreate("missing", "create-2"), nil); code != http.StatusNotFound {
		t.Errorf("unknown profile: status %d (%s), want 404", code, body)
	}
	if code, body := f.do("POST", "/v1alpha1/runs", alice, devTestCreate("notready", "create-3"), nil); code != http.StatusConflict {
		t.Errorf("not-Ready profile: status %d (%s), want 409", code, body)
	}
	for name, body := range map[string]any{
		"no clientKey": map[string]any{"profile": "ready", "task": map[string]any{"prompt": "p"}},
		"no prompt":    map[string]any{"profile": "ready", "clientKey": "k"},
		"unknown":      map[string]any{"profile": "ready", "clientKey": "k", "task": map[string]any{"prompt": "p"}, "extra": 1},
		"bad repo":     map[string]any{"profile": "ready", "clientKey": "k", "task": map[string]any{"prompt": "p"}, "workspace": map[string]any{"repo": "nope", "baseRef": "main"}},
		"not json":     json.RawMessage(`{"profile":`),
	} {
		if code, resp := f.do("POST", "/v1alpha1/runs", alice, body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d (%s), want 400", name, code, resp)
		}
	}
}

func testDevOwnership(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	run := f.createRun(f.token("alice"), devTestCreate("ready", "own-1"), http.StatusCreated)
	bob := f.token("bob")
	for _, rq := range []struct{ method, path string }{
		{"GET", "/v1alpha1/runs/" + run.Name},
		{"GET", "/v1alpha1/runs/" + run.Name + "/events"},
		{"POST", "/v1alpha1/runs/" + run.Name + "/steer"},
		{"POST", "/v1alpha1/runs/" + run.Name + "/abort"},
	} {
		var body any
		if strings.HasSuffix(rq.path, "/steer") {
			body = wire.Steer{Message: "hi"}
		}
		if code, resp := f.do(rq.method, rq.path, bob, body, nil); code != http.StatusNotFound {
			t.Errorf("%s %s as another owner: status %d (%s), want 404", rq.method, rq.path, code, resp)
		}
	}
	for _, r := range f.listRuns(bob) {
		if r.Name == run.Name {
			t.Errorf("another owner lists %s", run.Name)
		}
	}
	mine := f.listRuns(f.token("alice"))
	found := false
	for _, r := range mine {
		found = found || r.Name == run.Name
	}
	if !found {
		t.Errorf("owner's list %+v lacks %s", mine, run.Name)
	}
	if f.getRunObject(run.Name).Status.Phase.Terminal() {
		t.Errorf("another owner's abort changed the run")
	}
}

func testDevEventsResumeAndEnd(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	alice := f.token("alice")
	run := f.createRun(alice, devTestCreate("ready", "events-1"), http.StatusCreated)
	for i := range 5 {
		f.hub.AppendEvent(run.Name, wire.Event{Type: wire.EventText, Data: json.RawMessage(`{"delta":"` + string(rune('a'+i)) + `"}`)})
	}
	resp := f.stream(alice, run.Name, strconv.FormatInt(f.base+2, 10))
	defer func() { _ = resp.Body.Close() }()
	next, stop := iter.Pull2(wire.ReadEvents(resp.Body))
	defer stop()
	for want := f.base + 3; want <= f.base+5; want++ {
		m, err, _ := next()
		if err != nil || m.ID != strconv.FormatInt(want, 10) || m.Type != wire.EventText {
			t.Fatalf("event = %+v, %v; want id %d text", m, err, want)
		}
	}
	f.hub.AppendEvent(run.Name, wire.Event{Type: wire.EventEnd, Data: json.RawMessage(`{"phase":"Succeeded","reason":"Completed","result":"ok","outputs":{}}`)})
	if m, err, _ := next(); err != nil || m.ID != strconv.FormatInt(f.base+6, 10) || m.Type != wire.EventEnd {
		t.Fatalf("event = %+v, %v; want id base+6 end", m, err)
	}
	if m, err, ok := next(); ok {
		t.Fatalf("after end got %+v, %v; want the stream to end", m, err)
	}
}

func testDevEventsHeartbeat(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	alice := f.token("alice")
	run := f.createRun(alice, devTestCreate("ready", "events-2"), http.StatusCreated)
	resp := f.stream(alice, run.Name, "")
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	if !f.clock.HasWaiters() {
		t.Fatal("event stream registered no heartbeat ticker")
	}
	f.clock.Step(wire.HeartbeatInterval)
	if line, err := br.ReadString('\n'); err != nil || line != ": hb\n" {
		t.Fatalf("line = %q, %v; want heartbeat", line, err)
	}
	f.hub.AppendEvent(run.Name, wire.Event{Type: wire.EventEnd, Data: json.RawMessage(`{"phase":"Failed","reason":"Cancelled","result":null,"outputs":{}}`)})
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), "id: "+strconv.FormatInt(f.base+1, 10)+"\nevent: end\n") {
		t.Fatalf("stream tail %q lacks the end event", rest)
	}
}

func testDevEventsTerminalWithoutLog(t *testing.T, de *devEnv) {
	setup := de.newFixture(t)
	alice := setup.token("alice")
	run := setup.createRun(alice, devTestCreate("ready", "events-3"), http.StatusCreated)
	setup.setStatus(run.Name, func(s *v1alpha1.AgentRunStatus) {
		s.Phase = v1alpha1.PhaseSucceeded
		s.Reason = v1alpha1.ReasonCompleted
		s.Result = &runtime.RawExtension{Raw: []byte(`{"answer":42}`)}
		s.Outputs = &v1alpha1.RunOutputs{Branch: "skua/" + run.Name}
	})
	f := de.newFixture(t)
	resp := f.stream(alice, run.Name, "")
	defer func() { _ = resp.Body.Close() }()
	next, stop := iter.Pull2(wire.ReadEvents(resp.Body))
	defer stop()
	m, err, _ := next()
	if err != nil || m.Type != wire.EventEnd {
		t.Fatalf("event = %+v, %v; want end", m, err)
	}
	var end wire.EndData
	if err := json.Unmarshal(m.Data, &end); err != nil {
		t.Fatal(err)
	}
	if end.Phase != "Succeeded" || end.Reason != "Completed" || string(end.Result) != `{"answer":42}` || end.Outputs.Branch != "skua/"+run.Name {
		t.Errorf("end = %+v", end)
	}
	if m, err, ok := next(); ok {
		t.Fatalf("after end got %+v, %v; want the stream to end", m, err)
	}
	if f.getRunObject(run.Name).Annotations[v1alpha1.AnnotationFetchedAt] == "" {
		t.Error("end from status did not set fetched-at")
	}
}

func testDevFetchedAt(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	alice := f.token("alice")
	run := f.createRun(alice, devTestCreate("ready", "fetch-1"), http.StatusCreated)
	f.getRun(alice, run.Name)
	if a := f.getRunObject(run.Name).Annotations[v1alpha1.AnnotationFetchedAt]; a != "" {
		t.Fatalf("non-terminal fetch set fetched-at %q", a)
	}
	f.setStatus(run.Name, func(s *v1alpha1.AgentRunStatus) {
		s.Phase = v1alpha1.PhaseFailed
		s.Reason = v1alpha1.ReasonAgentError
	})
	if got := f.getRun(alice, run.Name); got.Phase != "Failed" || got.Reason != v1alpha1.ReasonAgentError {
		t.Fatalf("run = %+v", got)
	}
	first := f.getRunObject(run.Name).Annotations[v1alpha1.AnnotationFetchedAt]
	if want := f.clock.Now().UTC().Format(time.RFC3339); first != want {
		t.Fatalf("fetched-at = %q, want %q", first, want)
	}
	f.clock.Step(30 * time.Minute)
	f.getRun(alice, run.Name)
	if again := f.getRunObject(run.Name).Annotations[v1alpha1.AnnotationFetchedAt]; again != first {
		t.Fatalf("second fetch changed fetched-at from %q to %q", first, again)
	}
}

func testDevSteer(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	alice := f.token("alice")
	steer := wire.Steer{Message: "focus on tests"}

	pending := f.createRun(alice, devTestCreate("ready", "steer-1"), http.StatusCreated)
	if code, body := f.do("POST", "/v1alpha1/runs/"+pending.Name+"/steer", alice, steer, nil); code != http.StatusConflict {
		t.Errorf("pending run without pod: status %d (%s), want 409", code, body)
	}

	running := f.createRun(alice, devTestCreate("ready", "steer-2"), http.StatusCreated)
	f.setStatus(running.Name, func(s *v1alpha1.AgentRunStatus) {
		s.Phase = v1alpha1.PhaseRunning
		s.PodName = "pod-steer"
	})
	if code, body := f.do("POST", "/v1alpha1/runs/"+running.Name+"/steer", alice, steer, nil); code != http.StatusAccepted {
		t.Fatalf("detached runner: status %d (%s), want 202", code, body)
	}
	conn, err := f.hub.AttachRunner(hub.Pod{Name: "pod-steer", UID: "u1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Detach()
	if b := conn.Backlog(); len(b) != 1 || b[0].Type != wire.CommandSteer || !bytes.Contains(b[0].Data, []byte("focus on tests")) {
		t.Fatalf("backlog = %+v, want the kept steer", b)
	}
	if code, body := f.do("POST", "/v1alpha1/runs/"+running.Name+"/steer", alice, wire.Steer{Message: "again"}, nil); code != http.StatusAccepted {
		t.Fatalf("attached runner: status %d (%s), want 202", code, body)
	}
	if c := devRecvCommand(t, conn); c.Type != wire.CommandSteer || !bytes.Contains(c.Data, []byte("again")) {
		t.Fatalf("command = %+v", c)
	}
	if code, body := f.do("POST", "/v1alpha1/runs/"+running.Name+"/steer", alice, wire.Steer{}, nil); code != http.StatusBadRequest {
		t.Errorf("empty message: status %d (%s), want 400", code, body)
	}

	f.setStatus(running.Name, func(s *v1alpha1.AgentRunStatus) {
		s.Phase = v1alpha1.PhaseSucceeded
		s.Reason = v1alpha1.ReasonCompleted
	})
	if code, body := f.do("POST", "/v1alpha1/runs/"+running.Name+"/steer", alice, steer, nil); code != http.StatusConflict {
		t.Errorf("terminal run: status %d (%s), want 409", code, body)
	}
}

func testDevAbortWithoutRunner(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	alice := f.token("alice")
	pending := f.createRun(alice, devTestCreate("ready", "abort-1"), http.StatusCreated)
	detached := f.createRun(alice, devTestCreate("ready", "abort-2"), http.StatusCreated)
	f.setStatus(detached.Name, func(s *v1alpha1.AgentRunStatus) {
		s.Phase = v1alpha1.PhaseRunning
		s.PodName = "pod-gone"
	})
	for _, name := range []string{pending.Name, detached.Name} {
		if code, body := f.do("POST", "/v1alpha1/runs/"+name+"/abort", alice, nil, nil); code != http.StatusAccepted {
			t.Fatalf("abort %s: status %d (%s), want 202", name, code, body)
		}
		run := f.getRunObject(name)
		if run.Status.Phase != v1alpha1.PhaseFailed || run.Status.Reason != v1alpha1.ReasonCancelled || run.Status.FinishedAt == nil {
			t.Errorf("run %s status = %+v, want Failed/Cancelled", name, run.Status)
		}
	}
}

func testDevAbortWithRunner(t *testing.T, de *devEnv) {
	f := de.newFixture(t)
	alice := f.token("alice")
	run := f.createRun(alice, devTestCreate("ready", "abort-3"), http.StatusCreated)
	f.setStatus(run.Name, func(s *v1alpha1.AgentRunStatus) {
		s.Phase = v1alpha1.PhaseRunning
		s.PodName = "pod-abort"
	})
	conn, err := f.hub.AttachRunner(hub.Pod{Name: "pod-abort", UID: "u2"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Detach()
	if code, body := f.do("POST", "/v1alpha1/runs/"+run.Name+"/abort", alice, nil, nil); code != http.StatusAccepted {
		t.Fatalf("abort: status %d (%s), want 202", code, body)
	}
	if c := devRecvCommand(t, conn); c.Type != wire.CommandAbort {
		t.Fatalf("command = %+v, want abort", c)
	}
	if p := f.getRunObject(run.Name).Status.Phase; p != v1alpha1.PhaseRunning {
		t.Fatalf("phase right after abort = %s, want Running", p)
	}
	f.clock.Step(dev.AbortGrace - time.Second)
	if p := f.getRunObject(run.Name).Status.Phase; p != v1alpha1.PhaseRunning {
		t.Fatalf("phase before the grace ends = %s, want Running", p)
	}
	f.clock.Step(time.Second)
	devEventually(t, func() bool {
		s := f.getRunObject(run.Name).Status
		return s.Phase == v1alpha1.PhaseFailed && s.Reason == v1alpha1.ReasonCancelled
	})
}
