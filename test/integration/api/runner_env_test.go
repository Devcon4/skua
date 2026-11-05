package api_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/auth/authtest"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/sandbox/sandboxtest"
)

const (
	runnerTestPool    = "skua-p"
	runnerTestVersion = "0.1.0"
	runnerTestKey     = "sk-skua-00000000000000000000000000000000"
)

var runnerTestModels = []string{"model-a", "model-b"}

type runnerTestEnv struct {
	c       client.Client
	cached  client.Client
	cfg     *config.Config
	hub     *hub.Hub
	clock   *clocktesting.FakeClock
	url     string
	http    *http.Client
	reviews *authtest.TokenReviews
}

func runnerToken(aud, ns, podName, podUID string) authtest.Token {
	return authtest.Token{Audience: aud, Namespace: ns, ServiceAccount: "default", PodName: podName, PodUID: podUID}
}

func runnerStartEnv(t *testing.T) *runnerTestEnv {
	t.Helper()
	restCfg, c, cfg := startAPIEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:     c.Scheme(),
		Logger:     testr.New(t),
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	sandboxtest.Start(t, mgr, sandboxtest.Options{})
	wg.Go(func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	})

	reviews := authtest.NewTokenReviews()
	clk := clocktesting.NewFakeClock(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	h := hub.New(clk)
	srv := startServer(t, restCfg, c, cfg, clk, h, reviews)
	e := &runnerTestEnv{c: c, cached: srv.cached, cfg: cfg, hub: h, clock: clk, url: srv.url, http: srv.http, reviews: reviews}
	e.createPool(t)
	return e
}

func (e *runnerTestEnv) createPool(t *testing.T) {
	t.Helper()
	ns := e.cfg.SandboxNamespace
	tmpl := &extv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerTestPool},
		Spec: extv1beta1.SandboxTemplateSpec{SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
			PodTemplate: sandboxv1beta1.PodTemplate{
				ObjectMeta: sandboxv1beta1.PodMetadata{Labels: map[string]string{sandbox.LabelProfile: "p"}},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{Name: "install-runner", Image: "skua:dev"}},
					Containers:     []corev1.Container{{Name: "agent", Image: "generic:dev"}},
				},
			},
		}},
	}
	pool := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerTestPool},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    ptr.To[int32](0),
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: runnerTestPool},
		},
	}
	for _, obj := range []client.Object{tmpl, pool} {
		if err := e.c.Create(t.Context(), obj); err != nil {
			t.Fatalf("create %T: %v", obj, err)
		}
	}
}

type runnerFixture struct {
	run   *v1alpha1.AgentRun
	pod   *corev1.Pod
	token string
}

type runnerFixtureOpts struct {
	forge     bool
	phase     v1alpha1.RunPhase
	secret    bool
	forgeTok  string
	maxResult int64
}

func (e *runnerTestEnv) fixture(t *testing.T, name string, o runnerFixtureOpts) *runnerFixture {
	t.Helper()
	ctx := t.Context()
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.cfg.RunNamespace, Name: name},
		Spec: v1alpha1.AgentRunSpec{
			Profile:   "p",
			ClientKey: name,
			Task:      v1alpha1.Task{Prompt: "do " + name},
		},
	}
	if o.forge {
		run.Spec.Workspace = &v1alpha1.RunWorkspace{Repo: "acme/widgets", BaseRef: "main"}
	}
	if err := e.c.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	claim := &extv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: e.cfg.SandboxNamespace,
			Name:      name,
			Labels:    map[string]string{sandbox.LabelRunName: name, sandbox.LabelRunUID: string(run.UID), sandbox.LabelProfile: "p"},
		},
		Spec: extv1beta1.SandboxClaimSpec{WarmPoolRef: extv1beta1.SandboxWarmPoolRef{Name: runnerTestPool}},
	}
	if err := e.c.Create(ctx, claim); err != nil {
		t.Fatalf("create claim: %v", err)
	}
	var pod corev1.Pod
	runnerEventually(t, "pod of claim "+name, func() bool {
		err := e.c.Get(ctx, client.ObjectKey{Namespace: e.cfg.SandboxNamespace, Name: name}, &pod)
		return err == nil && pod.Labels[extv1beta1.SandboxIDLabel] == string(claim.UID)
	})

	if o.secret {
		e.writeSecret(t, name, o.forgeTok)
	}
	e.setStatus(t, name, func(st *v1alpha1.AgentRunStatus) {
		st.Phase = o.phase
		st.Claim = name
		st.PodName = pod.Name
		st.PodUID = pod.UID
		st.ProfileSnapshot = runnerTestSnapshot(o)
	})
	tok := e.reviews.Add("tok-"+name, runnerToken(
		sandbox.TokenAudience, e.cfg.SandboxNamespace, pod.Name, string(pod.UID),
	))
	return &runnerFixture{run: e.waitRun(t, name, func(r *v1alpha1.AgentRun) bool { return r.Status.Phase == o.phase }), pod: &pod, token: tok}
}

func runnerTestSnapshot(o runnerFixtureOpts) *v1alpha1.AgentProfileSpec {
	forge := v1alpha1.ForgeNone
	if o.forge {
		forge = v1alpha1.ForgeGitHub
	}
	maxResult := o.maxResult
	if maxResult == 0 {
		maxResult = sandbox.DefaultMaxResultBytes
	}
	return &v1alpha1.AgentProfileSpec{
		Image:   "generic:dev",
		Command: []string{"omp"},
		Models:  runnerTestModels,
		Forge:   forge,
		Limits:  v1alpha1.Limits{MaxResultBytes: maxResult},
	}
}

func (e *runnerTestEnv) writeSecret(t *testing.T, run, forgeToken string) {
	t.Helper()
	ctx := t.Context()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: e.cfg.RunNamespace, Name: sandbox.SecretName(run)}}
	err := e.c.Get(ctx, client.ObjectKeyFromObject(secret), secret)
	exists := err == nil
	if err != nil && client.IgnoreNotFound(err) != nil {
		t.Fatalf("get secret: %v", err)
	}
	secret.Labels = map[string]string{sandbox.LabelRunSecret: "true", sandbox.LabelRunName: run}
	secret.Data = map[string][]byte{sandbox.SecretKeyLiteLLM: []byte(runnerTestKey)}
	if forgeToken != "" {
		secret.Data[sandbox.SecretKeyForgeToken] = []byte(forgeToken)
		secret.Data[sandbox.SecretKeyForgeExpiresAt] = []byte(runnerTestExpiry(forgeToken).Format(time.RFC3339))
	}
	if err := e.saveSecret(ctx, secret, exists); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	runnerEventually(t, "cached secret of "+run, func() bool {
		var got corev1.Secret
		return e.cached.Get(ctx, client.ObjectKeyFromObject(secret), &got) == nil &&
			string(got.Data[sandbox.SecretKeyForgeToken]) == forgeToken
	})
}

func (e *runnerTestEnv) saveSecret(ctx context.Context, secret *corev1.Secret, exists bool) error {
	if exists {
		return e.c.Update(ctx, secret)
	}
	return e.c.Create(ctx, secret)
}

func runnerTestExpiry(token string) time.Time {
	return time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC).Add(time.Duration(len(token)) * time.Minute)
}

func (e *runnerTestEnv) setStatus(t *testing.T, name string, mutate func(*v1alpha1.AgentRunStatus)) {
	t.Helper()
	var run v1alpha1.AgentRun
	if err := e.c.Get(t.Context(), client.ObjectKey{Namespace: e.cfg.RunNamespace, Name: name}, &run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	mutate(&run.Status)
	if err := e.c.Status().Update(t.Context(), &run); err != nil {
		t.Fatalf("update run status: %v", err)
	}
}

func (e *runnerTestEnv) waitRun(t *testing.T, name string, cond func(*v1alpha1.AgentRun) bool) *v1alpha1.AgentRun {
	t.Helper()
	var run v1alpha1.AgentRun
	runnerEventually(t, "cached run "+name, func() bool {
		err := e.cached.Get(t.Context(), client.ObjectKey{Namespace: e.cfg.RunNamespace, Name: name}, &run)
		return err == nil && cond(&run)
	})
	return &run
}

func (e *runnerTestEnv) getRun(t *testing.T, name string) *v1alpha1.AgentRun {
	t.Helper()
	var run v1alpha1.AgentRun
	if err := e.c.Get(t.Context(), client.ObjectKey{Namespace: e.cfg.RunNamespace, Name: name}, &run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	return &run
}
