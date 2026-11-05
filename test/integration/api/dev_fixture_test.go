package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/auth/authtest"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/wire"
)

type devFixture struct {
	t      *testing.T
	c      client.Client
	cfg    *config.Config
	issuer *authtest.Issuer
	clock  *testingclock.FakeClock
	hub    *hub.Hub
	base   int64
	url    string
	http   *http.Client
}

type devEnv struct {
	restCfg *rest.Config
	c       client.Client
	cfg     *config.Config
	issuer  *authtest.Issuer
}

func (de *devEnv) newFixture(t *testing.T) *devFixture {
	clk := testingclock.NewFakeClock(time.Now())
	base := clk.Now().UnixMicro()
	h := hub.New(clk)
	srv := startServer(t, de.restCfg, de.c, de.cfg, clk, h, authtest.NewTokenReviews())
	return &devFixture{t: t, c: de.c, cfg: de.cfg, issuer: de.issuer, clock: clk, hub: h, base: base, url: srv.url, http: srv.http}
}

func devTestProfile(t *testing.T, c client.Client, name string, ready bool) {
	t.Helper()
	ctx := context.Background()
	p := &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.AgentProfileSpec{
			Description: "profile " + name,
			Image:       "generic:dev",
			Command:     []string{"omp"},
			Models:      []string{"model-a"},
		},
	}
	if err := c.Create(ctx, p); err != nil {
		t.Fatalf("create profile %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := c.Delete(context.Background(), p); err != nil {
			t.Errorf("delete profile %s: %v", name, err)
		}
	})
	status, reason := metav1.ConditionFalse, v1alpha1.ReasonModelUnknown
	if ready {
		status, reason = metav1.ConditionTrue, v1alpha1.ReasonReady
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: v1alpha1.ConditionReady, Status: status, Reason: reason, ObservedGeneration: p.Generation,
	})
	if err := c.Status().Update(ctx, p); err != nil {
		t.Fatalf("set profile %s status: %v", name, err)
	}
}

func devTestCreate(profile, clientKey string) wire.CreateRunRequest {
	return wire.CreateRunRequest{Profile: profile, ClientKey: clientKey, Task: wire.Task{Prompt: "do the thing"}}
}

func (f *devFixture) token(sub string) string {
	return f.issuer.Token(sub, devTestAudience)
}

func (f *devFixture) do(method, path, token string, body any, header http.Header) (int, []byte) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, ok := body.(json.RawMessage)
		if !ok {
			var err error
			if raw, err = json.Marshal(body); err != nil {
				f.t.Fatal(err)
			}
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(f.t.Context(), method, f.url+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, out
}

func (f *devFixture) createRun(token string, req wire.CreateRunRequest, wantCode int) wire.Run {
	f.t.Helper()
	code, body := f.do("POST", "/v1alpha1/runs", token, req, nil)
	if code != wantCode {
		f.t.Fatalf("create run: status %d (%s), want %d", code, body, wantCode)
	}
	var run wire.Run
	if err := json.Unmarshal(body, &run); err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *devFixture) getRun(token, name string) wire.Run {
	f.t.Helper()
	code, body := f.do("GET", "/v1alpha1/runs/"+name, token, nil, nil)
	if code != http.StatusOK {
		f.t.Fatalf("get run %s: status %d (%s)", name, code, body)
	}
	var run wire.Run
	if err := json.Unmarshal(body, &run); err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *devFixture) listRuns(token string) []wire.Run {
	f.t.Helper()
	code, body := f.do("GET", "/v1alpha1/runs", token, nil, nil)
	if code != http.StatusOK {
		f.t.Fatalf("list runs: status %d (%s)", code, body)
	}
	var runs []wire.Run
	if err := json.Unmarshal(body, &runs); err != nil {
		f.t.Fatal(err)
	}
	return runs
}

func (f *devFixture) stream(token, name, lastEventID string) *http.Response {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	f.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", f.url+"/v1alpha1/runs/"+name+"/events", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		f.t.Fatalf("events: status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return resp
}

func (f *devFixture) getRunObject(name string) *v1alpha1.AgentRun {
	f.t.Helper()
	run := &v1alpha1.AgentRun{}
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: f.cfg.RunNamespace, Name: name}, run); err != nil {
		f.t.Fatalf("get run %s: %v", name, err)
	}
	return run
}

func (f *devFixture) setStatus(name string, mutate func(*v1alpha1.AgentRunStatus)) {
	f.t.Helper()
	run := f.getRunObject(name)
	mutate(&run.Status)
	if err := f.c.Status().Update(context.Background(), run); err != nil {
		f.t.Fatalf("update run %s status: %v", name, err)
	}
}

func devRecvCommand(t *testing.T, conn *hub.RunnerConn) wire.Command {
	t.Helper()
	select {
	case c, ok := <-conn.Commands():
		if !ok {
			t.Fatal("runner stream closed")
		}
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no command delivered")
	}
	return wire.Command{}
}

func devEventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
