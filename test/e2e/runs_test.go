//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	sandboxextv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
)

const (
	s26User    = "dev@skua.test"
	s26Fake    = "fake"
	s26Generic = "generic"
	s26Repo    = "e2e/sample"

	s26ForgeNamespace  = "skua-e2e"
	s26ForgeDeployment = "deployment/skua-e2e-forge"
	s26ForgeContainer  = "forge"

	s26UpstreamPrimary = "fake-primary"
	s26RunKeySecretKey = "litellmKey"

	s26RunTimeout = 5 * time.Minute

	s26ResearchSchema = `{
		"type": "object",
		"properties": {
			"title": {"type": "string", "minLength": 3},
			"summary": {"type": "string"},
			"confidence": {"type": "number", "minimum": 0, "maximum": 1}
		},
		"required": ["title", "summary", "confidence"],
		"additionalProperties": false
	}`
)

var s26SmallWorkspace = resource.MustParse("1Gi")

func TestPullRequestLocalForge(t *testing.T) {
	t.Parallel()
	c := s26Client(t)
	forge := newForgeClient(t)
	seed, ok := forge.refs(t, s26Repo)["refs/heads/main"]
	if !ok {
		t.Fatalf("forge repo %s has no main branch", s26Repo)
	}
	scenario := s26Scenario(t, "pr-local-forge")
	profile := tempProfile(t, s26Generic, func(s *v1alpha1.AgentProfileSpec) {
		s.Workspace.Size = s26SmallWorkspace
		s.WarmPool.Replicas = ptr.To[int32](1)
	})
	s26WaitPoolReady(t, profile, 1)
	before := len(fakellmRequests(t))

	run := startRun(t, c, profile, "FAKELLM:pr-local-forge Add HELLO.md, push it to the run branch and open a pull request.",
		runOpts{Workspace: &wire.Workspace{Repo: s26Repo, BaseRef: seed}})
	pod := podOf(t, run.Name)
	claim := s26GetClaim(t, run.Name)
	if !pod.CreationTimestamp.Before(&claim.CreationTimestamp) {
		t.Errorf("pod %s was created at %s, not before claim %s at %s: it was not adopted from the warm pool",
			pod.Name, pod.CreationTimestamp, claim.Name, claim.CreationTimestamp)
	}
	waitPhase(t, run.Name, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted, s26RunTimeout)

	got, code := c.run(t, run.Name)
	if code != http.StatusOK {
		t.Fatalf("GET run %s: status %d", run.Name, code)
	}
	branch := "skua/" + run.Name
	if got.Outputs.Branch != branch {
		t.Errorf("outputs.branch = %q, want %q", got.Outputs.Branch, branch)
	}
	pulls := forge.pulls(t, s26Repo)
	i := slices.IndexFunc(pulls, func(p forgePull) bool { return p.Head.Ref == branch })
	if i < 0 {
		t.Fatalf("forge has no pull request from %s: %+v", branch, pulls)
	}
	if got.Outputs.PullRequest != pulls[i].Number {
		t.Errorf("outputs.pullRequest = %d, want %d", got.Outputs.PullRequest, pulls[i].Number)
	}
	sha, ok := forge.refs(t, s26Repo)["refs/heads/"+branch]
	if !ok {
		t.Fatalf("forge repo %s has no refs/heads/%s", s26Repo, branch)
	}
	if files := s26ForgeFiles(t, s26Repo, sha); !slices.Contains(files, "HELLO.md") {
		t.Errorf("refs/heads/%s holds %v, want HELLO.md", branch, files)
	}

	served := s26ScenarioRequests(fakellmRequests(t)[before:], scenario.Name)
	for turn := range scenario.Turns {
		if !slices.ContainsFunc(served, func(r litellmtest.RequestRecord) bool {
			return r.Turn == turn && r.Status == http.StatusOK && r.Model == s26UpstreamPrimary
		}) {
			t.Errorf("fakellm served no %s request for turn %d of %s through LiteLLM: %+v", s26UpstreamPrimary, turn, scenario.Name, served)
		}
	}
}

func TestResearchSchemaResult(t *testing.T) {
	t.Parallel()
	c := s26Client(t)
	schema := s26CompileSchema(t, s26ResearchSchema)
	claude := tempProfile(t, s26Generic, func(s *v1alpha1.AgentProfileSpec) {
		s.Models = []string{"e2e-claude", "e2e-helper"}
		s.Workspace.Size = s26SmallWorkspace
	})
	for _, tc := range []struct{ name, profile string }{
		{name: "openai", profile: s26Generic},
		{name: "anthropic", profile: claude},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			models := s26ProfileModels(t, tc.profile)
			run := startRun(t, c, tc.profile, "FAKELLM:research-schema Explain why warm pools shorten run starts.",
				runOpts{Schema: json.RawMessage(s26ResearchSchema)})
			key := s26RunKey(t, run.Name)
			ar := waitPhase(t, run.Name, v1alpha1.PhaseSucceeded, "", s26RunTimeout)
			if ar.Status.Result == nil || len(ar.Status.Result.Raw) == 0 {
				t.Fatalf("run %s has no status.result", run.Name)
			}
			result := ar.Status.Result.Raw
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(result))
			if err != nil {
				t.Fatalf("status.result %s: %v", result, err)
			}
			if err := schema.Validate(inst); err != nil {
				t.Errorf("status.result %s does not validate: %v", result, err)
			}
			got, code := c.run(t, run.Name)
			if code != http.StatusOK {
				t.Fatalf("GET run %s: status %d", run.Name, code)
			}
			if !s26JSONEqual(t, got.Result, result) {
				t.Errorf("API result = %s, want status.result %s", got.Result, result)
			}
			s26AssertSpendModels(t, key, models)
		})
	}
}

func TestUpstreamFaults(t *testing.T) {
	// Resetting before t.Parallel clears the log while every other test is still paused.
	fakellmReset(t)
	t.Parallel()
	c := s26Client(t)
	scenario := s26Scenario(t, "upstream-429-then-ok")
	run := startRun(t, c, s26Generic, "FAKELLM:upstream-429-then-ok Say hello.", runOpts{})
	waitPhase(t, run.Name, v1alpha1.PhaseSucceeded, "", s26RunTimeout)
	served := s26ScenarioRequests(fakellmRequests(t), scenario.Name)
	if !slices.ContainsFunc(served, func(r litellmtest.RequestRecord) bool {
		return r.Status == http.StatusTooManyRequests
	}) {
		t.Errorf("fakellm never answered %s with 429 during run %s", scenario.Name, run.Name)
	}
	if !slices.ContainsFunc(served, func(r litellmtest.RequestRecord) bool {
		return r.Status == http.StatusOK
	}) {
		t.Errorf("fakellm never answered %s with 200 during run %s", scenario.Name, run.Name)
	}
}

func TestBudgetExceeded(t *testing.T) {
	t.Parallel()
	c := s26Client(t)
	profile := tempProfile(t, s26Fake, func(s *v1alpha1.AgentProfileSpec) {
		s.Limits.BudgetUSD = resource.MustParse("0.0001")
	})
	run := startRun(t, c, profile, "SCRIPT:llm e2e-primary 3", runOpts{})
	replies, last := s26Replies(t, c, run.Name)
	ar := waitPhase(t, run.Name, v1alpha1.PhaseFailed, v1alpha1.ReasonBudgetExceeded, s26RunTimeout)
	if replies == 0 {
		t.Fatalf("no upstream reply reached run %s before the budget refusal", run.Name)
	}
	if ar.Status.FinishedAt == nil {
		t.Fatalf("run %s has no status.finishedAt", run.Name)
	}
	if gap := ar.Status.FinishedAt.Sub(last); gap > time.Minute {
		t.Errorf("run %s turned Failed %s after its last upstream reply, want within 1m of the first refusal", run.Name, gap)
	}
}

func TestModelOutsideKeyRefused(t *testing.T) {
	t.Parallel()
	c := s26Client(t)
	refused := startRun(t, c, s26Fake, "SCRIPT:llm e2e-other 1", runOpts{})
	waitPhase(t, refused.Name, v1alpha1.PhaseFailed, v1alpha1.ReasonModelRefused, s26RunTimeout)

	sleeper := startRun(t, c, s26Fake, "SCRIPT:sleep 120", runOpts{})
	key := s26RunKey(t, sleeper.Name)
	waitPhase(t, sleeper.Name, v1alpha1.PhaseRunning, "", podTimeout)
	if code, body := s26Chat(t, key, "e2e-other"); code != http.StatusForbidden || !strings.Contains(body, "key_model_access_denied") {
		t.Errorf("run key on e2e-other: status %d: %s; want 403 key_model_access_denied", code, body)
	}
	if code, body := s26Chat(t, key, "e2e-primary"); code != http.StatusOK {
		t.Errorf("run key on e2e-primary: status %d: %s; want 200", code, body)
	}
}

func TestKeyRevokedAtEnd(t *testing.T) {
	t.Parallel()
	c := s26Client(t)
	profile := tempProfile(t, s26Fake, func(s *v1alpha1.AgentProfileSpec) {
		s.WarmPool.Replicas = ptr.To[int32](1)
	})
	run := startRun(t, c, profile, "SCRIPT:reply ok", runOpts{})
	key := s26RunKey(t, run.Name)
	ar := waitPhase(t, run.Name, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted, s26RunTimeout)

	waitKeyRevoked(t, run.Name, key, 30*time.Second)
	s26WaitGone(t, "claim "+run.Name, &sandboxextv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: sandboxNamespace, Name: run.Name},
	})
	if ar.Status.PodName == "" {
		t.Fatalf("run %s has no status.podName", run.Name)
	}
	s26WaitGone(t, "pod "+ar.Status.PodName, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: sandboxNamespace, Name: ar.Status.PodName},
	})
	s26WaitPoolReady(t, profile, 1)
}

func TestKeyRevokedMidRun(t *testing.T) {
	t.Parallel()
	c := s26Client(t)
	run := startRun(t, c, s26Fake, "SCRIPT:llm-loop 3s", runOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), s26RunTimeout)
	defer cancel()
	evs := s26Follow(t, ctx, c, run.Name)
	ar := waitPhase(t, run.Name, v1alpha1.PhaseRunning, "", podTimeout)
	s26AwaitReply(t, evs, run.Name)

	alias := "skua-" + string(ar.UID)
	if code, body := newLitellmAdmin(t).do(t, http.MethodPost, "/key/delete", map[string][]string{"key_aliases": {alias}}, nil); code != http.StatusOK {
		t.Fatalf("delete key alias %s: status %d: %s", alias, code, body)
	}
	waitPhase(t, run.Name, v1alpha1.PhaseFailed, v1alpha1.ReasonKeyRevoked, time.Minute)
}

func s26Client(t *testing.T) apiClient {
	t.Helper()
	return apiClient{token: login(t, s26User)}
}

func s26Scenario(t *testing.T, name string) litellmtest.Scenario {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "fakes", "llm", "scenarios", name+".yaml"))
	if err != nil {
		t.Fatalf("read scenario %s: %v", name, err)
	}
	sc, err := litellmtest.ParseScenario(data)
	if err != nil {
		t.Fatalf("parse scenario %s: %v", name, err)
	}
	return sc
}

func s26ScenarioRequests(records []litellmtest.RequestRecord, scenario string) []litellmtest.RequestRecord {
	var out []litellmtest.RequestRecord
	for _, r := range records {
		if r.Scenario == scenario {
			out = append(out, r)
		}
	}
	return out
}

func s26ProfileModels(t *testing.T, name string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var p v1alpha1.AgentProfile
	if err := k8s.Get(ctx, client.ObjectKey{Name: name}, &p); err != nil {
		t.Fatalf("get profile %s: %v", name, err)
	}
	return p.Spec.Models
}

func s26GetClaim(t *testing.T, run string) *sandboxextv1beta1.SandboxClaim {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var claim sandboxextv1beta1.SandboxClaim
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: sandboxNamespace, Name: run}, &claim); err != nil {
		t.Fatalf("get claim %s: %v", run, err)
	}
	return &claim
}

func s26WaitGone(t *testing.T, what string, obj client.Object) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	eventually(t, what+" gone", podTimeout, func(ctx context.Context) (bool, string) {
		err := k8s.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, "still present"
	})
}

func s26WaitPoolReady(t *testing.T, profile string, ready int32) {
	t.Helper()
	key := client.ObjectKey{Namespace: sandboxNamespace, Name: sandbox.Name(profile)}
	eventually(t, fmt.Sprintf("warm pool %s has %d ready", key.Name, ready), podTimeout, func(ctx context.Context) (bool, string) {
		var pool sandboxextv1beta1.SandboxWarmPool
		if err := k8s.Get(ctx, key, &pool); err != nil {
			return false, err.Error()
		}
		return pool.Status.ReadyReplicas >= ready, fmt.Sprintf("%d of %d ready", pool.Status.ReadyReplicas, pool.Status.Replicas)
	})
}

func s26RunKey(t *testing.T, run string) string {
	t.Helper()
	name := "skua-run-" + run
	var key string
	eventually(t, "Secret "+name, podTimeout, func(ctx context.Context) (bool, string) {
		s, err := clientset.CoreV1().Secrets(runNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		key = string(s.Data[s26RunKeySecretKey])
		return key != "", "no " + s26RunKeySecretKey + " yet"
	})
	return key
}

func s26Chat(t *testing.T, key, model string) (int, string) {
	t.Helper()
	body := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "Reply with ok."}},
	}
	code, resp := litellmRequest(t, key, http.MethodPost, "/v1/chat/completions", body, nil)
	return code, string(resp)
}

func s26AssertSpendModels(t *testing.T, key string, models []string) {
	t.Helper()
	admin := newLitellmAdmin(t)
	eventually(t, "LiteLLM spend logs of the run key", 2*time.Minute, func(context.Context) (bool, string) {
		var rows []struct {
			ModelGroup string `json:"model_group"`
		}
		code, body := admin.do(t, http.MethodGet, "/spend/logs?api_key="+url.QueryEscape(key), nil, &rows)
		if code != http.StatusOK {
			return false, fmt.Sprintf("/spend/logs status %d: %s", code, body)
		}
		if len(rows) == 0 {
			return false, "no spend logs yet"
		}
		for _, r := range rows {
			if !slices.Contains(models, r.ModelGroup) {
				t.Fatalf("spend log of the run key names model group %q, outside the profile's models %v", r.ModelGroup, models)
			}
		}
		return true, ""
	})
}

func s26CompileSchema(t *testing.T, raw string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	const loc = "result-schema.json"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(loc, doc); err != nil {
		t.Fatalf("schema: %v", err)
	}
	schema, err := compiler.Compile(loc)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func s26JSONEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

func s26ForgeFiles(t *testing.T, repo, rev string) []string {
	t.Helper()
	script := `git --git-dir "$(ls -d /tmp/fakeforge-*/` + repo + `.git)" ls-tree --name-only "$1"`
	var stdout, stderr bytes.Buffer
	cmd := kubectl("-n", s26ForgeNamespace, "exec", s26ForgeDeployment, "-c", s26ForgeContainer, "--", "sh", "-c", script, "sh", rev)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("list %s at %s in the forge: %v: %s", repo, rev, err, strings.TrimSpace(stderr.String()))
	}
	return strings.Fields(stdout.String())
}

func s26Follow(t *testing.T, ctx context.Context, c apiClient, run string) <-chan wire.LogEvent {
	t.Helper()
	s := c.openEvents(t, ctx, run, 0)
	out := make(chan wire.LogEvent)
	go func() {
		defer close(out)
		defer s.close()
		for {
			ev, err := s.next()
			if err != nil {
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
			if ev.Type == wire.EventEnd {
				return
			}
		}
	}()
	return out
}

func s26Replies(t *testing.T, c apiClient, run string) (int, time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), s26RunTimeout)
	defer cancel()
	n, last := 0, time.Time{}
	for ev := range s26Follow(t, ctx, c, run) {
		if ev.Type == wire.EventText {
			n++
			last = time.Now()
		}
	}
	return n, last
}

func s26AwaitReply(t *testing.T, evs <-chan wire.LogEvent, run string) {
	t.Helper()
	for ev := range evs {
		if ev.Type == wire.EventText {
			return
		}
	}
	t.Fatalf("events of run %s ended before an upstream reply", run)
}
