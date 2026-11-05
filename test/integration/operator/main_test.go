package operator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/operator"
	"github.com/Devcon4/skua/internal/testenv"
)

const testEnvLabel = "test.skua.dev/env"

var (
	sharedEnv *testenv.Env
	envSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	ctrl.SetLogger(logr.Discard())
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	env, err := testenv.Boot()
	switch {
	case errors.Is(err, testenv.ErrNoAssets):
		fmt.Fprintln(os.Stderr, "operator envtest tests skip:", err)
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 1
	default:
		sharedEnv = env
		defer func() {
			if err := env.Stop(); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
	}
	return m.Run()
}

func envtestClient(t *testing.T) (*rest.Config, client.Client) {
	t.Helper()
	if sharedEnv == nil {
		t.Skip(testenv.ErrNoAssets.Error())
	}
	return sharedEnv.Config, sharedEnv.Client
}

const operatorTestConfigYAML = `
runNamespace: skua
sandboxNamespace: skua-sandboxes
oidc: {issuerURL: http://dex.127.0.0.1.nip.io:32000, audience: skua-omp}
gateway:
  baseURL: http://litellm.gateway.svc:4000
  operatorKeySecret: {name: skua-litellm-operator, key: apiKey}
  egress: {namespace: gateway, podLabels: {app: litellm}, port: 4000}
api:
  runnerURL: http://skua-api.skua.svc
  egress: {namespace: skua, podLabels: {app.kubernetes.io/name: skua-api}, port: 8080}
github:
  appSecret: {namespace: flux-system, name: skua-test-github-app}
network: {podCIDRs: [10.244.0.0/16], serviceCIDRs: [10.96.0.0/16], nodeCIDRs: [172.18.0.0/16]}
retention: {afterFetch: 24h, max: 168h}
runner: {image: skua:dev, supportedVersions: ">=0.1.0 <0.2.0"}
`

func operatorTestConfig() *config.Config {
	cfg, err := config.Parse([]byte(operatorTestConfigYAML))
	if err != nil {
		panic(err)
	}
	return cfg
}

func isolatedConfig() *config.Config {
	cfg := operatorTestConfig()
	n := envSeq.Add(1)
	cfg.RunNamespace = fmt.Sprintf("skua-%d", n)
	cfg.SandboxNamespace = fmt.Sprintf("skua-sandboxes-%d", n)
	return cfg
}

func testProfileMeta(cfg *config.Config, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Labels: map[string]string{testEnvLabel: cfg.RunNamespace}}
}

func ensureRuntimeClass(t *testing.T, c client.Client, name string) {
	t.Helper()
	rc := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Handler: "runsc"}
	if err := c.Create(t.Context(), rc); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create runtime class %s: %v", name, err)
	}
}

func startOperatorEnv(t *testing.T, cfg *config.Config, setup func(ctrl.Manager) error) client.Client {
	t.Helper()
	restCfg, c := envtestClient(t)

	for _, ns := range []string{cfg.RunNamespace, cfg.SandboxNamespace} {
		if err := c.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}

	cacheOpts := operator.CacheOptions(cfg)
	cacheOpts.ByObject[&v1alpha1.AgentProfile{}] = cache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{testEnvLabel: cfg.RunNamespace}),
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:     c.Scheme(),
		Logger:     testr.New(t),
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Cache:      cacheOpts,
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	if err := setup(mgr); err != nil {
		t.Fatalf("set up reconciler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("manager stopped: %v", err)
		}
	})

	syncCtx, stopSync := context.WithTimeout(ctx, 30*time.Second)
	defer stopSync()
	if !mgr.GetCache().WaitForCacheSync(syncCtx) {
		t.Fatal("manager cache did not sync within 30s")
	}
	return c
}

func TestOperatorEnv(t *testing.T) {
	cfg := isolatedConfig()
	var got ctrl.Manager
	c := startOperatorEnv(t, cfg, func(mgr ctrl.Manager) error {
		got = mgr
		return nil
	})

	if got == nil || got.GetAPIReader() == nil {
		t.Fatalf("setup got manager %v, want a manager with an API reader", got)
	}
	for _, ns := range []string{cfg.RunNamespace, cfg.SandboxNamespace} {
		if err := c.Get(t.Context(), client.ObjectKey{Name: ns}, &corev1.Namespace{}); err != nil {
			t.Errorf("get namespace %s: %v", ns, err)
		}
	}
}
