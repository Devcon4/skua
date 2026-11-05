package api_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/Devcon4/skua/internal/api"
	"github.com/Devcon4/skua/internal/api/auth/authtest"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/testenv"
)

const apiTestConfig = `
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
network: {podCIDRs: [10.244.0.0/16], serviceCIDRs: [10.96.0.0/16], nodeCIDRs: [172.18.0.0/16], extraEgress: []}
defaults: {runtimeClassName: gvisor}
retention: {afterFetch: 24h, max: 168h}
runner: {image: skua:dev, supportedVersions: ">=0.1.0 <0.2.0"}
`

var (
	envOnce  sync.Once
	envBoot  bool
	envReady = make(chan struct{})
	env      *testenv.Env
	envErr   error
	nsSeq    atomic.Int32
)

func TestMain(m *testing.M) {
	ctrllog.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	code := m.Run()
	if envBoot {
		<-envReady
	}
	if env != nil {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "stop envtest:", err)
			code = 1
		}
	}
	os.Exit(code)
}

func bootAPIEnv() {
	envOnce.Do(func() {
		envBoot = true
		go func() {
			defer close(envReady)
			env, envErr = testenv.Boot()
		}()
	})
}

func startAPIEnv(t *testing.T) (*rest.Config, client.Client, *config.Config) {
	t.Helper()
	bootAPIEnv()
	<-envReady
	if errors.Is(envErr, testenv.ErrNoAssets) {
		t.Skip(envErr.Error())
	}
	if envErr != nil {
		t.Fatalf("start envtest: %v", envErr)
	}
	cfg, err := config.Parse([]byte(apiTestConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	n := nsSeq.Add(1)
	cfg.RunNamespace = fmt.Sprintf("%s-%d", cfg.RunNamespace, n)
	cfg.SandboxNamespace = fmt.Sprintf("%s-%d", cfg.SandboxNamespace, n)
	for _, ns := range []string{cfg.RunNamespace, cfg.SandboxNamespace} {
		if err := env.Client.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	return env.Config, env.Client, cfg
}

type apiServer struct {
	url    string
	http   *http.Client
	cached client.Client
}

func startServer(t *testing.T, restCfg *rest.Config, c client.Client, cfg *config.Config, clk clock.PassiveClock, h *hub.Hub, reviews *authtest.TokenReviews) *apiServer {
	t.Helper()
	informers, err := cache.New(restCfg, api.CacheOptions(cfg, c.Scheme()))
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	cached, err := client.New(restCfg, client.Options{Scheme: c.Scheme(), Cache: &client.CacheOptions{Reader: informers}})
	if err != nil {
		t.Fatalf("create cached client: %v", err)
	}
	srv, err := api.New(api.Deps{
		Config:       cfg,
		Client:       cached,
		Direct:       c,
		Cache:        informers,
		TokenReviews: reviews.Client(),
		Hub:          h,
		Clock:        clk,
		HTTPClient:   &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	var lc net.ListenConfig
	apiLn, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	healthLn, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, apiLn, healthLn) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	s := &apiServer{url: "http://" + apiLn.Addr().String(), http: &http.Client{}, cached: cached}
	readyz := "http://" + healthLn.Addr().String() + "/readyz"
	deadline := time.Now().Add(15 * time.Second)
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, readyz, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := s.http.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("cache did not sync")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
