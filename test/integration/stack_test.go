package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api"
	"github.com/Devcon4/skua/internal/api/auth/authtest"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/sandbox/sandboxtest"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/integration/github/githubtest"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
	"github.com/Devcon4/skua/internal/operator"
)

const (
	stackLabel    = "skua.test/stack"
	devUser       = "dev@skua.test"
	devAudience   = "skua-omp"
	model         = "e2e-primary"
	forgeRepo     = "acme/widget"
	forgeTokenTTL = 20 * time.Minute
	poll          = 20 * time.Millisecond
	waitTimeout   = 15 * time.Second
)

var stackSeq atomic.Int64

type stack struct {
	t       *testing.T
	id      string
	cfg     *config.Config
	clock   *clocktesting.FakeClock
	gateway *litellmtest.Fake
	forge   *githubtest.Fake
	binder  *sandboxtest.Binder
	kubelet *kubelet
	reviews *authtest.TokenReviews
	issuer  *authtest.Issuer
	profile *v1alpha1.AgentProfile
	devTok  string

	apiAddr string
	apiStop func()
}

type stackOptions struct {
	profile  func(*v1alpha1.AgentProfileSpec)
	warmPods int
	forge    bool
}

func newStack(t *testing.T, opts stackOptions) *stack {
	t.Helper()
	requireEnv(t)
	t.Parallel()
	id := fmt.Sprintf("s%d", stackSeq.Add(1))
	s := &stack{
		t:       t,
		id:      id,
		clock:   clocktesting.NewFakeClock(time.Now()),
		reviews: authtest.NewTokenReviews(),
		issuer:  authtest.New(t),
	}
	s.gateway = litellmtest.New(s.clock)
	s.gateway.AddModel(model, true, "fakellm")
	s.forge = githubtest.New(s.clock, forgeTokenTTL)
	s.devTok = s.issuer.Token(devUser, devAudience)

	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for skua-api: %v", err)
	}
	s.apiAddr = apiLn.Addr().String()
	llm := httptest.NewServer(litellmtest.NewServer(nil, clock.RealClock{}))
	t.Cleanup(llm.Close)
	forgeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/pulls") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(forgeAPI.Close)
	s.cfg = s.config(llm.URL, forgeAPI.URL, forgeRoot(t, opts.forge))
	for _, ns := range []string{s.cfg.RunNamespace, s.cfg.SandboxNamespace} {
		if err := env.Client.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}

	s.startOperator()
	s.startAPI(apiLn)
	s.createProfile(opts)
	if opts.warmPods > 0 {
		pool := client.ObjectKey{Namespace: s.cfg.SandboxNamespace, Name: sandbox.Name(s.profile.Name)}
		s.eventually("warm pool "+pool.Name, func(ctx context.Context) (bool, string) {
			err := env.Client.Get(ctx, pool, &extv1beta1.SandboxWarmPool{})
			return err == nil, fmt.Sprint(err)
		})
		s.binder.SetPoolReady(pool, opts.warmPods)
	}
	return s
}

func forgeRoot(t *testing.T, seed bool) string {
	t.Helper()
	root := t.TempDir()
	if !seed {
		return root
	}
	bare := filepath.Join(root, forgeRepo+".git")
	src := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", "-b", "main", bare},
		{"-C", src, "init", "--quiet", "-b", "main"},
		{"-C", src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "init"},
		{"-C", src, "push", "--quiet", bare, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func (s *stack) config(gatewayURL, forgeAPIURL, gitRoot string) *config.Config {
	s.t.Helper()
	raw := fmt.Sprintf(`
runNamespace: it-%[1]s
sandboxNamespace: it-%[1]s-sb
oidc: {issuerURL: %[2]q, audience: %[3]s}
gateway:
  baseURL: %[6]q
  operatorKeySecret: {name: skua-litellm-operator, key: apiKey}
  egress: {namespace: gateway, podLabels: {app: litellm}, port: 4000}
api:
  runnerURL: %[4]q
  egress: {namespace: skua, podLabels: {app.kubernetes.io/name: skua-api}, port: 8080}
github:
  appSecret: {namespace: flux-system, name: skua-test-github-app}
  apiURL: %[7]q
  gitURL: %[5]q
network: {podCIDRs: [10.244.0.0/16], serviceCIDRs: [10.96.0.0/16], nodeCIDRs: [172.18.0.0/16]}
runner: {image: skua:dev, supportedVersions: ">=0.1.0 <0.2.0"}
`, s.id, s.issuer.URL(), devAudience, "http://"+s.apiAddr, "file://"+gitRoot, gatewayURL, forgeAPIURL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		s.t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func (s *stack) startOperator() {
	t := s.t
	cacheOpts := operator.CacheOptions(s.cfg)
	cacheOpts.ByObject[&v1alpha1.AgentProfile{}] = cache.ByObject{Label: labels.SelectorFromSet(labels.Set{stackLabel: s.id})}
	mgr, err := ctrl.NewManager(env.Config, ctrl.Options{
		Scheme:     env.Client.Scheme(),
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
		Cache:      cacheOpts,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	err = operator.Register(mgr, operator.Deps{
		Config:    s.cfg,
		Gateway:   s.gateway,
		Forge:     s.forge,
		APIReader: mgr.GetAPIReader(),
		Clock:     s.clock,
		Timing:    operator.Timing{RetryMax: time.Second, PodPoll: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("register operator: %v", err)
	}
	s.binder = sandboxtest.Start(t, mgr, sandboxtest.Options{Clock: s.clock})
	s.kubelet = startKubelet(t, mgr, s.reviews)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("manager stopped: %v", err)
		}
	})
	syncCtx, stopSync := context.WithTimeout(ctx, waitTimeout)
	defer stopSync()
	if !mgr.GetCache().WaitForCacheSync(syncCtx) {
		t.Fatal("manager cache did not sync")
	}
	t.Cleanup(s.kubelet.stopAll)
}

func (s *stack) startAPI(apiLn net.Listener) {
	t := s.t
	healthLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for skua-api health: %v", err)
	}
	informers, err := cache.New(env.Config, api.CacheOptions(s.cfg, env.Client.Scheme()))
	if err != nil {
		t.Fatalf("create skua-api cache: %v", err)
	}
	cached, err := client.New(env.Config, client.Options{Scheme: env.Client.Scheme(), Cache: &client.CacheOptions{Reader: informers}})
	if err != nil {
		t.Fatalf("create skua-api cached client: %v", err)
	}
	srv, err := api.New(api.Deps{
		Config:       s.cfg,
		Client:       cached,
		Direct:       env.Client,
		Cache:        informers,
		TokenReviews: s.reviews.Client(),
		Hub:          hub.New(s.clock),
		Clock:        s.clock,
		HTTPClient:   &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("build skua-api: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, apiLn, healthLn) }()
	s.apiStop = sync.OnceFunc(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("skua-api stopped: %v", err)
		}
	})
	t.Cleanup(s.apiStop)
	readyz := "http://" + healthLn.Addr().String() + "/readyz"
	s.eventually("skua-api ready", func(ctx context.Context) (bool, string) {
		code, body := httpDo(ctx, http.MethodGet, readyz, "", nil, nil)
		return code == http.StatusOK, fmt.Sprintf("readyz %d %s", code, body)
	})
}

// The hub numbers events from its start time, so a replacement skua-api must see the clock move.
func (s *stack) restartAPI(gap time.Duration) {
	s.t.Helper()
	s.apiStop()
	s.clock.Step(gap)
	var ln net.Listener
	s.eventually("skua-api address free", func(context.Context) (bool, string) {
		var err error
		ln, err = net.Listen("tcp", s.apiAddr)
		return err == nil, fmt.Sprint(err)
	})
	s.startAPI(ln)
}

func (s *stack) createProfile(opts stackOptions) {
	t := s.t
	p := &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "it-" + s.id, Labels: map[string]string{stackLabel: s.id}},
		Spec: v1alpha1.AgentProfileSpec{
			Description: "in-process integration profile",
			Image:       "skua-it:dev",
			Command:     []string{"fakeomp", "--mode", "rpc"},
			Models:      []string{model},
			Limits:      v1alpha1.Limits{MaxRuntime: metav1.Duration{Duration: 30 * time.Minute}},
		},
	}
	if opts.warmPods > 0 {
		p.Spec.WarmPool.Replicas = ptr.To(int32(opts.warmPods))
	}
	if opts.forge {
		p.Spec.Forge = v1alpha1.ForgeGitHub
	}
	if opts.profile != nil {
		opts.profile(&p.Spec)
	}
	if err := env.Client.Create(t.Context(), p); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	s.eventually("profile "+p.Name+" Ready", func(ctx context.Context) (bool, string) {
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(p.Status.Conditions, v1alpha1.ConditionReady)
		if c == nil || c.Status != metav1.ConditionTrue || c.ObservedGeneration != p.Generation {
			return false, fmt.Sprintf("Ready = %+v", c)
		}
		return true, ""
	})
	s.profile = p
}

func (s *stack) eventually(what string, cond func(ctx context.Context) (ok bool, detail string)) {
	s.t.Helper()
	eventually(s.t, what, waitTimeout, cond)
}

func eventually(t *testing.T, what string, timeout time.Duration, cond func(ctx context.Context) (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		ok, detail := cond(ctx)
		cancel()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s: %s", what, timeout, detail)
		}
		time.Sleep(poll)
	}
}

func (s *stack) apiURL(path string) string {
	return "http://" + s.apiAddr + "/v1alpha1" + path
}

func httpDo(ctx context.Context, method, url, token string, body any, header http.Header) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, []byte(err.Error())
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, []byte(err.Error())
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (s *stack) dev(method, path string, body any) (int, []byte) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.t.Context(), 10*time.Second)
	defer cancel()
	return httpDo(ctx, method, s.apiURL(path), s.devTok, body, nil)
}

func (s *stack) startRun(prompt string, ws *wire.Workspace) wire.Run {
	s.t.Helper()
	req := wire.CreateRunRequest{
		Profile:   s.profile.Name,
		ClientKey: fmt.Sprintf("%s-%d", s.t.Name(), time.Now().UnixNano()),
		Task:      wire.Task{Prompt: prompt},
		Workspace: ws,
	}
	code, body := s.dev(http.MethodPost, "/runs", req)
	if code != http.StatusCreated {
		s.t.Fatalf("create run: status %d: %s", code, body)
	}
	var run wire.Run
	if err := json.Unmarshal(body, &run); err != nil {
		s.t.Fatalf("decode run: %v: %s", err, body)
	}
	return run
}

func (s *stack) steer(run, msg string) int {
	s.t.Helper()
	code, _ := s.dev(http.MethodPost, "/runs/"+run+"/steer", wire.Steer{Message: msg})
	return code
}

func (s *stack) abort(run string) int {
	s.t.Helper()
	code, _ := s.dev(http.MethodPost, "/runs/"+run+"/abort", nil)
	return code
}

func (s *stack) waitPhase(name string, phase v1alpha1.RunPhase, reason string) *v1alpha1.AgentRun {
	s.t.Helper()
	var ar v1alpha1.AgentRun
	s.eventually(fmt.Sprintf("run %s %s/%s", name, phase, reason), func(ctx context.Context) (bool, string) {
		if err := env.Client.Get(ctx, client.ObjectKey{Namespace: s.cfg.RunNamespace, Name: name}, &ar); err != nil {
			return false, err.Error()
		}
		if ar.Status.Phase == phase && (reason == "" || ar.Status.Reason == reason) {
			return true, ""
		}
		if ar.Status.Phase.Terminal() {
			s.t.Fatalf("run %s is %s/%s (%s), want %s/%s", name, ar.Status.Phase, ar.Status.Reason, ar.Status.Message, phase, reason)
		}
		return false, fmt.Sprintf("phase %s/%s", ar.Status.Phase, ar.Status.Reason)
	})
	return &ar
}

func (s *stack) running(prompt string) (*v1alpha1.AgentRun, client.ObjectKey) {
	s.t.Helper()
	run := s.startRun(prompt, nil)
	ar := s.waitPhase(run.Name, v1alpha1.PhaseRunning, "")
	return ar, s.podKey(ar)
}

func (s *stack) podKey(ar *v1alpha1.AgentRun) client.ObjectKey {
	return client.ObjectKey{Namespace: s.cfg.SandboxNamespace, Name: ar.Status.PodName}
}

func (s *stack) claimKey(run string) client.ObjectKey {
	return client.ObjectKey{Namespace: s.cfg.SandboxNamespace, Name: run}
}

func (s *stack) waitGone(what string, key client.ObjectKey, obj client.Object) {
	s.t.Helper()
	s.eventually(what+" gone", func(ctx context.Context) (bool, string) {
		err := env.Client.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("still present, deletionTimestamp %v", obj.GetDeletionTimestamp())
	})
}

// Stands in for a RequeueAfter on real time, which stepping the fake clock does not shorten.
func (s *stack) nudge(name string) {
	s.t.Helper()
	ar := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: s.cfg.RunNamespace, Name: name}}
	patch := fmt.Appendf(nil, `{"metadata":{"annotations":{"skua.test/nudge":%q}}}`, strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := env.Client.Patch(s.t.Context(), ar, client.RawPatch(types.MergePatchType, patch)); err != nil {
		s.t.Fatalf("nudge run %s: %v", name, err)
	}
}

func (s *stack) runKey(name string) string {
	s.t.Helper()
	var key string
	s.eventually("secret of run "+name+" holds its LiteLLM key", func(ctx context.Context) (bool, string) {
		var sec corev1.Secret
		if err := env.Client.Get(ctx, client.ObjectKey{Namespace: s.cfg.RunNamespace, Name: sandbox.SecretName(name)}, &sec); err != nil {
			return false, err.Error()
		}
		key = string(sec.Data[sandbox.SecretKeyLiteLLM])
		return key != "", "no " + sandbox.SecretKeyLiteLLM
	})
	return key
}

type eventStream struct {
	next func() (wire.Message, error, bool)
	stop func()
	body io.Closer
}

func (s *stack) openEvents(ctx context.Context, run string, lastID int64) (*eventStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiURL("/runs/"+run+"/events"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+s.devTok)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("events %s: status %d: %s", run, resp.StatusCode, b)
	}
	next, stop := iter.Pull2(wire.ReadEvents(resp.Body))
	return &eventStream{next: next, stop: stop, body: resp.Body}, nil
}

func (es *eventStream) read() (wire.LogEvent, error) {
	m, err, ok := es.next()
	if !ok {
		return wire.LogEvent{}, io.EOF
	}
	if err != nil {
		return wire.LogEvent{}, err
	}
	id, err := strconv.ParseInt(m.ID, 10, 64)
	if err != nil {
		return wire.LogEvent{}, fmt.Errorf("event id %q: %w", m.ID, err)
	}
	return wire.LogEvent{ID: id, Event: wire.Event{Type: m.Type, Data: m.Data}}, nil
}

func (es *eventStream) close() {
	_ = es.body.Close()
	es.stop()
}

func readUntil(t *testing.T, es *eventStream, got *[]wire.LogEvent, what string, stop func(wire.LogEvent) bool) {
	t.Helper()
	for {
		ev, err := es.read()
		if err != nil {
			t.Fatalf("events: waiting for %s after %d events: %v", what, len(*got), err)
		}
		*got = append(*got, ev)
		if stop(ev) {
			return
		}
		if ev.Type == wire.EventEnd {
			t.Fatalf("events: stream ended before %s: %s", what, ev.Data)
		}
	}
}

func isEnd(ev wire.LogEvent) bool {
	return ev.Type == wire.EventEnd
}

func isText(want string) func(wire.LogEvent) bool {
	return func(ev wire.LogEvent) bool {
		s, ok := eventText(ev)
		return ok && s == want
	}
}

func eventText(ev wire.LogEvent) (string, bool) {
	if ev.Type != wire.EventText {
		return "", false
	}
	var d wire.TextData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return "", false
	}
	return d.Delta, true
}

func dumpOnFailure(t *testing.T, paths func() []string) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, p := range paths() {
			if b, err := os.ReadFile(p); err == nil {
				t.Logf("%s:\n%s", p, b)
			}
		}
	})
}
