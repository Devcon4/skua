package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/api/runnerapi"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func TestRunner(t *testing.T) {
	bootAPIEnv()
	t.Parallel()
	e := runnerStartEnv(t)

	t.Run("wrong audience is TokenInvalid", func(t *testing.T) { testRunnerWrongAudience(t, e) })

	t.Run("pod uid mismatch is PodMismatch", func(t *testing.T) { testRunnerPodUIDMismatch(t, e) })

	t.Run("pod outside the sandbox namespace is PodMismatch", func(t *testing.T) { testRunnerPodOutsideNamespace(t, e) })

	t.Run("pod whose claim uid label differs from its claim is refused", func(t *testing.T) { testRunnerClaimUIDMismatch(t, e) })

	t.Run("unsupported version fails the bound run", func(t *testing.T) { testRunnerUnsupportedVersion(t, e) })

	t.Run("second stream is 409", func(t *testing.T) { testRunnerSecondStream(t, e) })

	t.Run("idle stream gets heartbeats until the run is Running with its Secret, then assign", func(t *testing.T) { testRunnerIdleStreamHeartbeats(t, e) })

	t.Run("Running run without its Secret waits for it", func(t *testing.T) { testRunnerWaitsForSecret(t, e) })

	t.Run("forge token change pushes token and a reattach gets assign and the current token", func(t *testing.T) { testRunnerForgeTokenPush(t, e) })

	t.Run("events for another run are 403 and own events are acked", func(t *testing.T) { testRunnerEventsOwnership(t, e) })

	t.Run("events of a type the runner may not send are 400", func(t *testing.T) { testRunnerEventTypeRefused(t, e) })

	t.Run("events over the body cap or the log budget are 413", func(t *testing.T) { testRunnerEventsTooLarge(t, e) })

	t.Run("events for a terminal run are 409", func(t *testing.T) { testRunnerEventsTerminalRun(t, e) })

	t.Run("result body over the cap is 413 and fails the run ResultTooLarge", func(t *testing.T) { testRunnerResultBodyTooLarge(t, e) })

	t.Run("oversized result is 413 and fails the run ResultTooLarge", func(t *testing.T) { testRunnerResultTooLarge(t, e) })

	t.Run("valid result is stored before 200 and ends the event log", func(t *testing.T) { testRunnerValidResult(t, e) })
}

func testRunnerWrongAudience(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "aud", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	tok := e.reviews.Add("tok-aud-other", runnerToken(
		"kubernetes", e.cfg.SandboxNamespace, f.pod.Name, string(f.pod.UID),
	))
	code, body := e.request(t, http.MethodPost, wire.RunnerEventsPath, tok, runnerTestVersion, wire.RunnerEventsRequest{Run: "aud"})
	runnerWantRefusal(t, code, body, http.StatusUnauthorized, wire.RefusalTokenInvalid)
}

func testRunnerPodUIDMismatch(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "uid", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	tok := e.reviews.Add("tok-uid-other", runnerToken(
		sandbox.TokenAudience, e.cfg.SandboxNamespace, f.pod.Name, "00000000-0000-0000-0000-000000000000",
	))
	code, body := e.request(t, http.MethodGet, wire.RunnerStreamPath, tok, runnerTestVersion, nil)
	runnerWantRefusal(t, code, body, http.StatusForbidden, wire.RefusalPodMismatch)
}

func testRunnerPodOutsideNamespace(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "ns", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	tok := e.reviews.Add("tok-ns-other", runnerToken(
		sandbox.TokenAudience, e.cfg.RunNamespace, f.pod.Name, string(f.pod.UID),
	))
	code, body := e.request(t, http.MethodGet, wire.RunnerStreamPath, tok, runnerTestVersion, nil)
	runnerWantRefusal(t, code, body, http.StatusForbidden, wire.RefusalPodMismatch)
}

func testRunnerClaimUIDMismatch(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "claimuid", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	patch := client.MergeFrom(f.pod.DeepCopy())
	f.pod.Labels[extv1beta1.SandboxIDLabel] = "11111111-1111-1111-1111-111111111111"
	if err := e.c.Patch(t.Context(), f.pod, patch); err != nil {
		t.Fatalf("patch pod: %v", err)
	}
	code, body := e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, runnerTestVersion,
		wire.RunnerEventsRequest{Run: "claimuid", Events: []wire.RunnerEvent{{Seq: 1, Type: wire.EventText, Data: json.RawMessage(`{"delta":"x"}`)}}})
	runnerWantRefusal(t, code, body, http.StatusForbidden, wire.RefusalRunMismatch)
}

func testRunnerUnsupportedVersion(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "version", runnerFixtureOpts{phase: v1alpha1.PhaseRunning, secret: true})
	code, body := e.request(t, http.MethodGet, wire.RunnerStreamPath, f.token, "0.0.1", nil)
	runnerWantRefusal(t, code, body, http.StatusBadRequest, v1alpha1.ReasonRunnerUnsupported)
	code, body = e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, "", wire.RunnerEventsRequest{Run: "version"})
	runnerWantRefusal(t, code, body, http.StatusBadRequest, v1alpha1.ReasonRunnerUnsupported)
	run := e.getRun(t, "version")
	if run.Status.Phase != v1alpha1.PhaseFailed || run.Status.Reason != v1alpha1.ReasonRunnerUnsupported {
		t.Fatalf("run = %s/%s, want Failed/RunnerUnsupported", run.Status.Phase, run.Status.Reason)
	}
}

func testRunnerSecondStream(t *testing.T, e *runnerTestEnv) {
	f := e.fixture(t, "second", runnerFixtureOpts{phase: v1alpha1.PhasePending})
	s := e.openStream(t, f.token, 0)
	e.heartbeat(t, s)
	code, body := e.request(t, http.MethodGet, wire.RunnerStreamPath, f.token, runnerTestVersion, nil)
	runnerWantRefusal(t, code, body, http.StatusConflict, wire.RefusalStreamActive)
}

func testRunnerIdleStreamHeartbeats(t *testing.T, e *runnerTestEnv) {
	f := e.fixture(t, "idle", runnerFixtureOpts{phase: v1alpha1.PhasePending})
	s := e.openStream(t, f.token, 0)
	e.heartbeat(t, s)
	e.writeSecret(t, "idle", "")
	e.heartbeat(t, s)
	e.setStatus(t, "idle", func(st *v1alpha1.AgentRunStatus) { st.Phase = v1alpha1.PhaseRunning })
	var a wire.Assign
	s.command(t, wire.CommandAssign, &a)
	if a.Run != "idle" || a.Task.Prompt != "do idle" || a.Forge != nil {
		t.Fatalf("assign = %+v", a)
	}
	if a.Gateway.Key != runnerTestKey || a.Gateway.BaseURL != e.cfg.Gateway.BaseURL || !slices.Equal(a.Gateway.Models, runnerTestModels) {
		t.Fatalf("assign gateway = %+v", a.Gateway)
	}
	if _, err := e.hub.AttachRunner(hub.Pod{Name: f.pod.Name, UID: string(f.pod.UID)}, 0); !errors.Is(err, hub.ErrStreamActive) {
		t.Fatalf("hub attach of pod %s = %v; want its stream still attached", f.pod.Name, err)
	}
}

func testRunnerWaitsForSecret(t *testing.T, e *runnerTestEnv) {
	f := e.fixture(t, "nosecret", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	s := e.openStream(t, f.token, 0)
	e.heartbeat(t, s)
	e.writeSecret(t, "nosecret", "")
	var a wire.Assign
	s.command(t, wire.CommandAssign, &a)
	if a.Gateway.Key != runnerTestKey {
		t.Fatalf("assign key = %q", a.Gateway.Key)
	}
}

func testRunnerForgeTokenPush(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "forge", runnerFixtureOpts{phase: v1alpha1.PhaseRunning, forge: true, secret: true, forgeTok: "ghs-first"})
	s := e.openStream(t, f.token, 0)
	var a wire.Assign
	s.command(t, wire.CommandAssign, &a)
	if a.Forge == nil || a.Forge.Token != "ghs-first" || !a.Forge.ExpiresAt.Equal(runnerTestExpiry("ghs-first")) {
		t.Fatalf("assign forge = %+v", a.Forge)
	}
	if a.Workspace == nil || a.Workspace.Repo != "acme/widgets" {
		t.Fatalf("assign workspace = %+v", a.Workspace)
	}

	e.writeSecret(t, "forge", "ghs-second")
	var tok wire.Token
	last := s.command(t, wire.CommandToken, &tok)
	if tok.Token != "ghs-second" || !tok.ExpiresAt.Equal(runnerTestExpiry("ghs-second")) {
		t.Fatalf("token = %+v", tok)
	}

	s.close()
	runnerEventually(t, "stream detached", func() bool {
		conn, err := e.hub.AttachRunner(hub.Pod{Name: f.pod.Name, UID: string(f.pod.UID)}, 0)
		if err != nil {
			return false
		}
		conn.Detach()
		return true
	})
	s = e.openStream(t, f.token, last)
	a = wire.Assign{}
	s.command(t, wire.CommandAssign, &a)
	if a.Forge == nil || a.Forge.Token != "ghs-second" {
		t.Fatalf("reattach assign forge = %+v", a.Forge)
	}
	tok = wire.Token{}
	s.command(t, wire.CommandToken, &tok)
	if tok.Token != "ghs-second" {
		t.Fatalf("reattach token = %+v", tok)
	}
}

func testRunnerEventsOwnership(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "events", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	evs := []wire.RunnerEvent{
		{Seq: 2, Type: wire.EventText, Data: json.RawMessage(`{"delta":"b"}`)},
		{Seq: 1, Type: wire.EventText, Data: json.RawMessage(`{"delta":"a"}`)},
	}
	code, body := e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, runnerTestVersion,
		wire.RunnerEventsRequest{Run: "aud", Events: evs})
	runnerWantRefusal(t, code, body, http.StatusForbidden, wire.RefusalRunMismatch)

	code, body = e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, runnerTestVersion,
		wire.RunnerEventsRequest{Run: "events", Events: evs})
	var ack wire.Ack
	if code != http.StatusOK || json.Unmarshal([]byte(body), &ack) != nil || ack.Ack != 2 {
		t.Fatalf("events = %d %q, want 200 ack 2", code, body)
	}
}

func testRunnerEventTypeRefused(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "evtype", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	for _, typ := range []string{wire.EventEnd, wire.EventStatus, "text\nevent: end", ""} {
		code, body := e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, runnerTestVersion, wire.RunnerEventsRequest{
			Run: "evtype", Events: []wire.RunnerEvent{{Seq: 1, Type: typ, Data: json.RawMessage(`{"delta":"x"}`)}},
		})
		runnerWantRefusal(t, code, body, http.StatusBadRequest, wire.RefusalBadRequest)
	}
	if ack, err := e.hub.AppendRunnerEvents("evtype", nil); err != nil || ack != 0 {
		t.Fatalf("ack = %d, %v; want no runner event stored", ack, err)
	}
}

func testRunnerEventsTooLarge(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "evbig", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	big, err := json.Marshal(wire.TextData{Delta: strings.Repeat("x", wire.MaxEventsBodyBytes)})
	if err != nil {
		t.Fatal(err)
	}
	code, body := e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, runnerTestVersion, wire.RunnerEventsRequest{
		Run: "evbig", Events: []wire.RunnerEvent{{Seq: 1, Type: wire.EventText, Data: big}},
	})
	runnerWantRefusal(t, code, body, http.StatusRequestEntityTooLarge, wire.RefusalBodyTooLarge)

	fill := json.RawMessage(strings.Repeat("1", hub.EventLogBudget))
	if _, err := e.hub.AppendRunnerEvents("evbig", []wire.RunnerEvent{{Seq: 1, Type: wire.EventText, Data: fill}}); err == nil {
		t.Fatal("an event over the budget was stored")
	}
	code, body = e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, runnerTestVersion, wire.RunnerEventsRequest{
		Run: "evbig", Events: []wire.RunnerEvent{{Seq: 2, Type: wire.EventText, Data: json.RawMessage(`{"delta":"x"}`)}},
	})
	runnerWantRefusal(t, code, body, http.StatusRequestEntityTooLarge, wire.RefusalEventLogFull)
}

func testRunnerEventsTerminalRun(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "evdone", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	code, body := e.request(t, http.MethodPost, wire.RunnerResultPath, f.token, runnerTestVersion, wire.RunnerResult{
		Run: "evdone", Phase: string(v1alpha1.PhaseSucceeded), Reason: v1alpha1.ReasonCompleted,
	})
	if code != http.StatusOK {
		t.Fatalf("result = %d %q", code, body)
	}
	e.waitRun(t, "evdone", func(r *v1alpha1.AgentRun) bool { return r.Status.Phase.Terminal() })
	code, body = e.request(t, http.MethodPost, wire.RunnerEventsPath, f.token, runnerTestVersion, wire.RunnerEventsRequest{
		Run: "evdone", Events: []wire.RunnerEvent{{Seq: 1, Type: wire.EventText, Data: json.RawMessage(`{"delta":"late"}`)}},
	})
	runnerWantRefusal(t, code, body, http.StatusConflict, wire.RefusalRunFinished)
}

func testRunnerResultBodyTooLarge(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "hugebody", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	big, err := json.Marshal(strings.Repeat("x", runnerapi.MaxResultBodyBytes))
	if err != nil {
		t.Fatal(err)
	}
	code, body := e.request(t, http.MethodPost, wire.RunnerResultPath, f.token, runnerTestVersion, wire.RunnerResult{
		Run: "hugebody", Phase: string(v1alpha1.PhaseSucceeded), Reason: v1alpha1.ReasonCompleted, Result: big,
	})
	runnerWantRefusal(t, code, body, http.StatusRequestEntityTooLarge, v1alpha1.ReasonResultTooLarge)
	if run := e.getRun(t, "hugebody"); run.Status.Phase != v1alpha1.PhaseFailed || run.Status.Reason != v1alpha1.ReasonResultTooLarge {
		t.Fatalf("run = %s/%s, want Failed/ResultTooLarge", run.Status.Phase, run.Status.Reason)
	}
}

func testRunnerResultTooLarge(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "large", runnerFixtureOpts{phase: v1alpha1.PhaseRunning, maxResult: 262144})
	big, err := json.Marshal(strings.Repeat("x", 300<<10))
	if err != nil {
		t.Fatal(err)
	}
	code, body := e.request(t, http.MethodPost, wire.RunnerResultPath, f.token, runnerTestVersion, wire.RunnerResult{
		Run: "large", Phase: string(v1alpha1.PhaseSucceeded), Reason: v1alpha1.ReasonCompleted, Result: big,
	})
	runnerWantRefusal(t, code, body, http.StatusRequestEntityTooLarge, v1alpha1.ReasonResultTooLarge)
	run := e.getRun(t, "large")
	if run.Status.Phase != v1alpha1.PhaseFailed || run.Status.Reason != v1alpha1.ReasonResultTooLarge || run.Status.Result != nil {
		t.Fatalf("run = %s/%s result %v, want Failed/ResultTooLarge without result", run.Status.Phase, run.Status.Reason, run.Status.Result)
	}
}

func testRunnerValidResult(t *testing.T, e *runnerTestEnv) {
	t.Parallel()
	f := e.fixture(t, "result", runnerFixtureOpts{phase: v1alpha1.PhaseRunning})
	res := wire.RunnerResult{
		Run: "result", Phase: string(v1alpha1.PhaseSucceeded), Reason: v1alpha1.ReasonCompleted,
		Result: json.RawMessage(`{"answer":42}`), Outputs: wire.Outputs{Branch: "skua/result", PullRequest: 7},
	}
	code, body := e.request(t, http.MethodPost, wire.RunnerResultPath, f.token, runnerTestVersion, res)
	if code != http.StatusOK {
		t.Fatalf("result = %d %q", code, body)
	}
	run := e.getRun(t, "result")
	if run.Status.Phase != v1alpha1.PhaseSucceeded || run.Status.Reason != v1alpha1.ReasonCompleted ||
		run.Status.Result == nil || string(run.Status.Result.Raw) != `{"answer":42}` ||
		run.Status.Outputs == nil || run.Status.Outputs.Branch != "skua/result" || run.Status.FinishedAt == nil {
		t.Fatalf("run status = %+v", run.Status)
	}

	code, _ = e.request(t, http.MethodPost, wire.RunnerResultPath, f.token, runnerTestVersion, wire.RunnerResult{
		Run: "result", Phase: string(v1alpha1.PhaseFailed), Reason: v1alpha1.ReasonAgentError,
	})
	if run := e.getRun(t, "result"); code != http.StatusOK || run.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("second result = %d, phase %s", code, run.Status.Phase)
	}

	var log []wire.LogEvent
	runnerEventually(t, "end event", func() bool {
		var cancel func()
		log, _, cancel = e.hub.Subscribe("result", 0)
		cancel()
		return len(log) > 0 && log[len(log)-1].Type == wire.EventEnd
	})
	if len(log) < 2 || log[len(log)-2].Type != wire.EventStatus {
		t.Fatalf("log = %+v, want status then end", log)
	}
	var status wire.StatusData
	if err := json.Unmarshal(log[len(log)-2].Data, &status); err != nil || status.Phase != "Succeeded" || status.Reason != v1alpha1.ReasonCompleted {
		t.Fatalf("status = %s, %v", log[len(log)-2].Data, err)
	}
	var end wire.EndData
	if err := json.Unmarshal(log[len(log)-1].Data, &end); err != nil || end.Phase != "Succeeded" ||
		string(end.Result) != `{"answer":42}` || end.Outputs.PullRequest != 7 {
		t.Fatalf("end = %s, %v", log[len(log)-1].Data, err)
	}
}
