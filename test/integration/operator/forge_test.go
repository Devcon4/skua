package operator_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/integration/github"
	"github.com/Devcon4/skua/internal/integration/github/githubtest"
	"github.com/Devcon4/skua/internal/operator/forge"
)

const forgeTestRepo = "acme/widgets"

var forgeTestStart = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

type forgeTestEnv struct {
	c     client.Client
	cfg   *config.Config
	clock *clocktesting.FakeClock
	fake  *githubtest.Fake
}

func forgeStart(t *testing.T) forgeTestEnv {
	t.Helper()
	t.Parallel()
	cfg := isolatedConfig()
	clk := clocktesting.NewFakeClock(forgeTestStart)
	fake := githubtest.New(clk, time.Hour)
	c := startOperatorEnv(t, cfg, func(mgr ctrl.Manager) error {
		return forge.Setup(mgr, forgeDeps(mgr, cfg, fake, clk))
	})
	return forgeTestEnv{c: c, cfg: cfg, clock: clk, fake: fake}
}

func forgeDeps(mgr ctrl.Manager, cfg *config.Config, minter github.Minter, clk clock.PassiveClock) forge.Deps {
	return forge.Deps{
		Config:    cfg,
		Forge:     minter,
		APIReader: mgr.GetAPIReader(),
		Clock:     clk,
		Timing:    forge.Timing{RetryMax: 500 * time.Millisecond},
	}
}

func forgeCreateRun(t *testing.T, c client.Client, cfg *config.Config, name string, kind v1alpha1.Forge, repo string) *v1alpha1.AgentRun {
	t.Helper()
	ctx := t.Context()
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.RunNamespace},
		Spec: v1alpha1.AgentRunSpec{
			Profile:   "coder",
			ClientKey: name,
			Task:      v1alpha1.Task{Prompt: "fix the build"},
		},
	}
	if repo != "" {
		run.Spec.Workspace = &v1alpha1.RunWorkspace{Repo: repo, BaseRef: "main"}
	}
	if err := c.Create(ctx, run); err != nil {
		t.Fatalf("create run %s: %v", name, err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      sandbox.SecretName(name),
		Namespace: cfg.RunNamespace,
		Labels:    map[string]string{sandbox.LabelRunSecret: "true", sandbox.LabelRunName: name},
	}}
	if err := controllerutil.SetControllerReference(run, secret, c.Scheme()); err != nil {
		t.Fatalf("own secret: %v", err)
	}
	if err := c.Create(ctx, secret); err != nil {
		t.Fatalf("create secret %s: %v", secret.Name, err)
	}
	run.Status = v1alpha1.AgentRunStatus{
		Phase: v1alpha1.PhasePending,
		ProfileSnapshot: &v1alpha1.AgentProfileSpec{
			Image:   "registry.example/agent@sha256:" + strings.Repeat("0", 64),
			Command: []string{"omp"},
			Models:  []string{"fake-model"},
			Forge:   kind,
		},
	}
	if err := c.Status().Update(ctx, run); err != nil {
		t.Fatalf("set run %s status: %v", name, err)
	}
	return run
}

func forgeGetRun(t *testing.T, c client.Client, cfg *config.Config, name string) *v1alpha1.AgentRun {
	t.Helper()
	run := &v1alpha1.AgentRun{}
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: cfg.RunNamespace, Name: name}, run); err != nil {
		t.Fatalf("get run %s: %v", name, err)
	}
	return run
}

func forgeGetSecret(t *testing.T, c client.Client, cfg *config.Config, run string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: cfg.RunNamespace, Name: sandbox.SecretName(run)}, secret); err != nil {
		t.Fatalf("get secret of run %s: %v", run, err)
	}
	return secret
}

func forgeLiveValues(t *testing.T, secret *corev1.Secret) []string {
	t.Helper()
	var live []struct {
		Token string `json:"token"`
	}
	if raw := secret.Data[forge.SecretKeyTokensLive]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &live); err != nil {
			t.Fatalf("parse live tokens: %v", err)
		}
	}
	values := make([]string, len(live))
	for i, l := range live {
		values[i] = l.Token
	}
	return values
}

func forgePoke(t *testing.T, c client.Client, cfg *config.Config, name string) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := forgeGetRun(t, c, cfg, name)
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations["skua.dev/test-poke"] = strconv.FormatInt(time.Now().UnixNano(), 10)
		return c.Update(t.Context(), run)
	})
	if err != nil {
		t.Fatalf("poke run %s: %v", name, err)
	}
}

func forgeEndRun(t *testing.T, c client.Client, cfg *config.Config, name string) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := forgeGetRun(t, c, cfg, name)
		run.Status.Phase = v1alpha1.PhaseSucceeded
		run.Status.Reason = v1alpha1.ReasonCompleted
		return c.Status().Update(t.Context(), run)
	})
	if err != nil {
		t.Fatalf("end run %s: %v", name, err)
	}
}

func forgeEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func forgeConsistently(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s stopped holding", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func forgeCondition(run *v1alpha1.AgentRun) *metav1.Condition {
	return meta.FindStatusCondition(run.Status.Conditions, forge.ConditionType)
}

func TestForgeMintRemintAndRevoke(t *testing.T) {
	env := forgeStart(t)
	const name = "coder-remint"
	forgeCreateRun(t, env.c, env.cfg, name, v1alpha1.ForgeGitHub, forgeTestRepo)

	forgeEventually(t, "first token", func() bool {
		return len(forgeGetSecret(t, env.c, env.cfg, name).Data[sandbox.SecretKeyForgeToken]) > 0
	})
	toks := env.fake.Tokens()
	if len(toks) != 1 {
		t.Fatalf("minted %d tokens, want 1", len(toks))
	}
	secret := forgeGetSecret(t, env.c, env.cfg, name)
	if got := string(secret.Data[sandbox.SecretKeyForgeToken]); got != toks[0].Value {
		t.Fatalf("forgeToken %q, want %q", got, toks[0].Value)
	}
	if got, want := string(secret.Data[sandbox.SecretKeyForgeExpiresAt]), forgeTestStart.Add(time.Hour).Format(time.RFC3339); got != want {
		t.Fatalf("forgeExpiresAt %q, want %q", got, want)
	}
	if !env.fake.Valid(toks[0].Value, forgeTestRepo) {
		t.Fatal("first token is not valid for the run's repository")
	}
	if !controllerutil.ContainsFinalizer(forgeGetRun(t, env.c, env.cfg, name), forge.Finalizer) {
		t.Fatalf("run lacks finalizer %s", forge.Finalizer)
	}
	if !controllerutil.ContainsFinalizer(forgeGetSecret(t, env.c, env.cfg, name), forge.Finalizer) {
		t.Fatalf("run Secret lacks finalizer %s once a token is stored", forge.Finalizer)
	}

	env.clock.Step(29 * time.Minute)
	forgePoke(t, env.c, env.cfg, name)
	forgeConsistently(t, "one token at 29 minutes", func() bool { return len(env.fake.Tokens()) == 1 })

	env.clock.Step(time.Minute)
	forgePoke(t, env.c, env.cfg, name)
	forgeEventually(t, "second token at 30 minutes", func() bool {
		return len(forgeLiveValues(t, forgeGetSecret(t, env.c, env.cfg, name))) == 2
	})
	toks = env.fake.Tokens()
	if len(toks) != 2 || !toks[1].IssuedAt.Equal(forgeTestStart.Add(30*time.Minute)) {
		t.Fatalf("tokens %+v, want a second one issued at 30 minutes", toks)
	}
	secret = forgeGetSecret(t, env.c, env.cfg, name)
	if got := string(secret.Data[sandbox.SecretKeyForgeToken]); got != toks[1].Value {
		t.Fatalf("forgeToken %q, want the second token %q", got, toks[1].Value)
	}
	if got := forgeLiveValues(t, secret); got[0] != toks[0].Value || got[1] != toks[1].Value {
		t.Fatalf("forgeTokensLive %v, want both tokens", got)
	}

	env.clock.Step(30 * time.Minute)
	forgePoke(t, env.c, env.cfg, name)
	forgeEventually(t, "third token at 60 minutes", func() bool { return len(env.fake.Tokens()) == 3 })
	toks = env.fake.Tokens()
	forgeEventually(t, "live list without the expired token", func() bool {
		got := forgeLiveValues(t, forgeGetSecret(t, env.c, env.cfg, name))
		return len(got) == 2 && got[0] == toks[1].Value && got[1] == toks[2].Value
	})

	forgeEndRun(t, env.c, env.cfg, name)
	forgeEventually(t, "finalizer released", func() bool {
		return !controllerutil.ContainsFinalizer(forgeGetRun(t, env.c, env.cfg, name), forge.Finalizer)
	})
	if got := forgeLiveValues(t, forgeGetSecret(t, env.c, env.cfg, name)); len(got) != 0 {
		t.Fatalf("forgeTokensLive %v after the run ended, want empty", got)
	}
	toks = env.fake.Tokens()
	if !toks[1].Revoked || !toks[2].Revoked {
		t.Fatalf("tokens %+v, want both live tokens revoked", toks)
	}
	if controllerutil.ContainsFinalizer(forgeGetSecret(t, env.c, env.cfg, name), forge.Finalizer) {
		t.Fatalf("run Secret keeps finalizer %s after revocation", forge.Finalizer)
	}
}

func TestForgeRevokesWhenRunSecretIsDeletedFirst(t *testing.T) {
	env := forgeStart(t)
	const name = "coder-secret-first"
	forgeCreateRun(t, env.c, env.cfg, name, v1alpha1.ForgeGitHub, forgeTestRepo)
	forgeEventually(t, "first token", func() bool {
		return len(forgeGetSecret(t, env.c, env.cfg, name).Data[sandbox.SecretKeyForgeToken]) > 0
	})

	if err := env.c.Delete(t.Context(), forgeGetSecret(t, env.c, env.cfg, name)); err != nil {
		t.Fatalf("delete run Secret: %v", err)
	}
	forgeEndRun(t, env.c, env.cfg, name)
	forgeEventually(t, "run Secret gone", func() bool {
		err := env.c.Get(t.Context(), client.ObjectKey{Namespace: env.cfg.RunNamespace, Name: sandbox.SecretName(name)}, &corev1.Secret{})
		return apierrors.IsNotFound(err)
	})
	if toks := env.fake.Tokens(); len(toks) != 1 || !toks[0].Revoked {
		t.Fatalf("tokens %+v, want the one token revoked", toks)
	}
	forgeEventually(t, "run finalizer released", func() bool {
		return !controllerutil.ContainsFinalizer(forgeGetRun(t, env.c, env.cfg, name), forge.Finalizer)
	})
}

func TestForgeRevokesOrphanedRunSecret(t *testing.T) {
	env := forgeStart(t)
	const name = "coder-orphaned"
	forgeCreateRun(t, env.c, env.cfg, name, v1alpha1.ForgeGitHub, forgeTestRepo)
	forgeEventually(t, "first token", func() bool {
		return len(forgeGetSecret(t, env.c, env.cfg, name).Data[sandbox.SecretKeyForgeToken]) > 0
	})

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := forgeGetSecret(t, env.c, env.cfg, name)
		secret.OwnerReferences = nil
		return env.c.Update(t.Context(), secret)
	})
	if err != nil {
		t.Fatalf("orphan run Secret: %v", err)
	}
	if err := env.c.Delete(t.Context(), forgeGetRun(t, env.c, env.cfg, name)); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	forgeEventually(t, "run gone", func() bool {
		err := env.c.Get(t.Context(), client.ObjectKey{Namespace: env.cfg.RunNamespace, Name: name}, &v1alpha1.AgentRun{})
		return apierrors.IsNotFound(err)
	})
	if toks := env.fake.Tokens(); len(toks) != 1 || !toks[0].Revoked {
		t.Fatalf("tokens %+v, want the one token revoked", toks)
	}
	forgeEventually(t, "orphaned run Secret without the forge finalizer", func() bool {
		return !controllerutil.ContainsFinalizer(forgeGetSecret(t, env.c, env.cfg, name), forge.Finalizer)
	})
}

func TestForgeRateLimit(t *testing.T) {
	env := forgeStart(t)

	env.fake.RateLimit(forgeTestStart.Add(2 * time.Minute))
	const brief = "coder-brief"
	forgeCreateRun(t, env.c, env.cfg, brief, v1alpha1.ForgeGitHub, forgeTestRepo)
	forgeEventually(t, "rate-limited condition", func() bool {
		c := forgeCondition(forgeGetRun(t, env.c, env.cfg, brief))
		return c != nil && c.Reason == forge.ReasonRateLimited
	})
	if n := len(env.fake.Tokens()); n != 0 {
		t.Fatalf("minted %d tokens while rate limited", n)
	}

	env.clock.Step(2 * time.Minute)
	forgePoke(t, env.c, env.cfg, brief)
	forgeEventually(t, "token after the rate limit", func() bool {
		return len(forgeGetSecret(t, env.c, env.cfg, brief).Data[sandbox.SecretKeyForgeToken]) > 0
	})
	if run := forgeGetRun(t, env.c, env.cfg, brief); run.Status.Phase.Terminal() {
		t.Fatalf("run %s ended %s/%s after a 2-minute rate limit", brief, run.Status.Phase, run.Status.Reason)
	}

	env.fake.RateLimit(env.clock.Now().Add(10 * time.Minute))
	const long = "coder-long"
	forgeCreateRun(t, env.c, env.cfg, long, v1alpha1.ForgeGitHub, forgeTestRepo)
	forgeEventually(t, "rate-limited condition", func() bool {
		c := forgeCondition(forgeGetRun(t, env.c, env.cfg, long))
		return c != nil && c.Reason == forge.ReasonRateLimited
	})
	env.clock.Step(5 * time.Minute)
	forgePoke(t, env.c, env.cfg, long)
	forgeEventually(t, "ForgeError after 5 minutes", func() bool {
		return forgeGetRun(t, env.c, env.cfg, long).Status.Phase == v1alpha1.PhaseFailed
	})
	run := forgeGetRun(t, env.c, env.cfg, long)
	if run.Status.Reason != v1alpha1.ReasonForgeError || !strings.Contains(run.Status.Message, "rate limited") || run.Status.FinishedAt == nil {
		t.Fatalf("run ended %s %q finishedAt %v, want ForgeError naming the rate limit with finishedAt", run.Status.Reason, run.Status.Message, run.Status.FinishedAt)
	}
	forgeEventually(t, "finalizer released", func() bool {
		return !controllerutil.ContainsFinalizer(forgeGetRun(t, env.c, env.cfg, long), forge.Finalizer)
	})
	if n := len(env.fake.Tokens()); n != 1 {
		t.Fatalf("minted %d tokens, want only the first run's", n)
	}
}

func TestForgeNoneMintsNothing(t *testing.T) {
	env := forgeStart(t)
	forgeCreateRun(t, env.c, env.cfg, "coder-none", v1alpha1.ForgeNone, forgeTestRepo)
	forgeCreateRun(t, env.c, env.cfg, "coder-noworkspace", v1alpha1.ForgeGitHub, "")
	forgeCreateRun(t, env.c, env.cfg, "coder-barrier", v1alpha1.ForgeGitHub, forgeTestRepo)

	forgeEventually(t, "barrier run token", func() bool {
		return len(forgeGetSecret(t, env.c, env.cfg, "coder-barrier").Data[sandbox.SecretKeyForgeToken]) > 0
	})
	forgeConsistently(t, "one token", func() bool { return len(env.fake.Tokens()) == 1 })
	for _, name := range []string{"coder-none", "coder-noworkspace"} {
		if data := forgeGetSecret(t, env.c, env.cfg, name).Data; len(data) != 0 {
			t.Errorf("run %s Secret has keys %v, want none", name, data)
		}
		if run := forgeGetRun(t, env.c, env.cfg, name); len(run.Finalizers) != 0 || forgeCondition(run) != nil {
			t.Errorf("run %s has finalizers %v and conditions %v, want none", name, run.Finalizers, run.Status.Conditions)
		}
	}
}

func TestForgeGitHubUnsetFailsRun(t *testing.T) {
	t.Parallel()
	cfg := isolatedConfig()
	cfg.GitHub = nil
	c := startOperatorEnv(t, cfg, func(mgr ctrl.Manager) error {
		return forge.Setup(mgr, forgeDeps(mgr, cfg, nil, clocktesting.NewFakeClock(forgeTestStart)))
	})
	const name = "coder-nogithub"
	forgeCreateRun(t, c, cfg, name, v1alpha1.ForgeGitHub, forgeTestRepo)

	forgeEventually(t, "ForgeError", func() bool {
		return forgeGetRun(t, c, cfg, name).Status.Phase == v1alpha1.PhaseFailed
	})
	run := forgeGetRun(t, c, cfg, name)
	if run.Status.Reason != v1alpha1.ReasonForgeError || len(run.Finalizers) != 0 || run.Status.FinishedAt == nil {
		t.Fatalf("run ended %s with finalizers %v finishedAt %v, want ForgeError, none and finishedAt", run.Status.Reason, run.Finalizers, run.Status.FinishedAt)
	}
}

func TestForgeAppSecretThroughAPIReader(t *testing.T) {
	t.Parallel()
	cfg := isolatedConfig()
	clk := clocktesting.NewFakeClock(forgeTestStart)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/42/access_tokens" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_apireader",
			"expires_at": clk.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(srv.Close)
	cfg.GitHub.APIURL = srv.URL

	var mgr ctrl.Manager
	c := startOperatorEnv(t, cfg, func(m ctrl.Manager) error {
		mgr = m
		d := forgeDeps(m, cfg, nil, clk)
		d.Forge = github.NewMinter(d.APIReader, *d.Config.GitHub, srv.Client(), d.Clock)
		return forge.Setup(m, d)
	})

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ref := cfg.GitHub.AppSecret
	if err := c.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ref.Namespace}}); err != nil {
		t.Fatalf("create namespace %s: %v", ref.Namespace, err)
	}
	appSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace},
		Data: map[string][]byte{
			ref.AppIDKey:          []byte("1234"),
			ref.InstallationIDKey: []byte("42"),
			ref.PrivateKeyKey:     pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		},
	}
	if err := c.Create(t.Context(), appSecret); err != nil {
		t.Fatalf("create app secret: %v", err)
	}
	if err := mgr.GetClient().Get(t.Context(), client.ObjectKeyFromObject(appSecret), &corev1.Secret{}); err == nil {
		t.Fatal("cached client read the App Secret; the test cannot tell cached from uncached reads")
	}

	const name = "coder-apireader"
	forgeCreateRun(t, c, cfg, name, v1alpha1.ForgeGitHub, forgeTestRepo)
	forgeEventually(t, "token minted with the App Secret", func() bool {
		return string(forgeGetSecret(t, c, cfg, name).Data[sandbox.SecretKeyForgeToken]) == "ghs_apireader"
	})
}
