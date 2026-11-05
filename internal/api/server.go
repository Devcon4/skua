// Package api is skua-api: the dev-facing and runner-facing HTTP API and its in-memory event hub.
package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	authenticationv1client "k8s.io/client-go/kubernetes/typed/authentication/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/dev"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/api/runnerapi"
	"github.com/Devcon4/skua/internal/api/watch"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/integration/kube"
)

const (
	defaultAPIAddr    = ":8080"
	defaultHealthAddr = ":8081"
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
	oidcTimeout       = 30 * time.Second
)

// Deps are skua-api's dependencies. Client reads through Cache, which must not be started yet: Serve starts it
// and gates /readyz on its sync. Direct reads and writes uncached; the dev routes use it, and the runner routes
// read pods and claims through it. A nil HTTPClient uses http.DefaultClient for OIDC discovery.
type Deps struct {
	Config       *config.Config
	Client       client.Client
	Direct       client.Client
	Cache        cache.Cache
	TokenReviews authenticationv1client.TokenReviewInterface
	Hub          *hub.Hub
	Clock        clock.PassiveClock
	HTTPClient   *http.Client
}

// Server serves the skua-api routes on :8080 and the health probes on :8081.
type Server struct {
	cache      cache.Cache
	mux        *http.ServeMux
	apiAddr    string
	healthAddr string
	ready      atomic.Bool

	mu       sync.Mutex
	stopping bool
	streams  map[*streamWriter]struct{}
}

// Run serves skua-api for cfg with the ambient kubeconfig until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubeconfig: %w", err)
	}
	scheme, err := kube.Scheme()
	if err != nil {
		return fmt.Errorf("scheme: %w", err)
	}
	direct, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	informers, err := cache.New(restCfg, CacheOptions(cfg, scheme))
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	cached, err := client.New(restCfg, client.Options{Scheme: scheme, Cache: &client.CacheOptions{Reader: informers}})
	if err != nil {
		return fmt.Errorf("cached client: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("clientset: %w", err)
	}
	clk := clock.RealClock{}
	srv, err := New(Deps{
		Config:       cfg,
		Client:       cached,
		Direct:       direct,
		Cache:        informers,
		TokenReviews: clientset.AuthenticationV1().TokenReviews(),
		Hub:          hub.New(clk),
		Clock:        clk,
		HTTPClient:   &http.Client{Timeout: oidcTimeout},
	})
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

// CacheOptions is the cache skua-api needs for cfg: AgentRuns and the labelled per-run Secrets in the run
// namespace, anything else in the run and sandbox namespaces.
func CacheOptions(cfg *config.Config, scheme *runtime.Scheme) cache.Options {
	runNamespace := map[string]cache.Config{cfg.RunNamespace: {}}
	return cache.Options{
		Scheme:                      scheme,
		ReaderFailOnMissingInformer: true,
		DefaultNamespaces:           map[string]cache.Config{cfg.RunNamespace: {}, cfg.SandboxNamespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.AgentRun{}: {Namespaces: runNamespace},
			&corev1.Secret{}: {
				Namespaces: runNamespace,
				Label:      labels.SelectorFromSet(labels.Set{sandbox.LabelRunSecret: "true"}),
			},
		},
	}
}

// New registers the dev routes, the runner routes and the watch that feeds AgentRun and Secret changes to them.
func New(d Deps) (*Server, error) {
	if d.Cache == nil {
		return nil, errors.New("api: no cache")
	}
	s := newServer(d.Cache)
	runner, runnerErr := runnerapi.Register(s, runnerapi.Deps{
		Config:       d.Config,
		Client:       d.Client,
		APIReader:    d.Direct,
		TokenReviews: d.TokenReviews,
		Hub:          d.Hub,
		Clock:        d.Clock,
	})
	err := errors.Join(
		dev.Register(s, dev.Deps{Config: d.Config, Client: d.Direct, Hub: d.Hub, Clock: d.Clock, HTTPClient: d.HTTPClient}),
		runnerErr,
	)
	if err != nil {
		return nil, err
	}
	if err := watch.Register(d.Cache, watch.Deps{Config: d.Config, Client: d.Client, Hub: d.Hub, Runner: runner}); err != nil {
		return nil, err
	}
	return s, nil
}

func newServer(c cache.Cache) *Server {
	return &Server{
		cache:      c,
		mux:        http.NewServeMux(),
		apiAddr:    defaultAPIAddr,
		healthAddr: defaultHealthAddr,
		streams:    map[*streamWriter]struct{}{},
	}
}

// Handle registers h for pattern (net/http ServeMux syntax) on the API port.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// Run listens on :8080 and :8081 and calls Serve.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	apiLn, err := lc.Listen(ctx, "tcp", s.apiAddr)
	if err != nil {
		return fmt.Errorf("api: listen %s: %w", s.apiAddr, err)
	}
	healthLn, err := lc.Listen(ctx, "tcp", s.healthAddr)
	if err != nil {
		_ = apiLn.Close()
		return fmt.Errorf("api: listen %s: %w", s.healthAddr, err)
	}
	return s.Serve(ctx, apiLn, healthLn)
}

// Serve starts the cache, serves the API routes on apiLn and the health probes on healthLn until ctx is
// cancelled, then closes event streams and shuts both listeners down gracefully. A response with Content-Type
// text/event-stream has its request context cancelled at shutdown. Serve returns early with an error when a
// listener or the cache fails. Both listeners are closed when it returns.
func (s *Server) Serve(ctx context.Context, apiLn, healthLn net.Listener) error {
	apiSrv := &http.Server{Handler: s.trackStreams(s.mux), ReadHeaderTimeout: readHeaderTimeout}
	healthSrv := &http.Server{Handler: s.healthHandler(), ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 3)
	serve := func(srv *http.Server, ln net.Listener) {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("api: serve %s: %w", ln.Addr(), err)
		}
	}
	go serve(apiSrv, apiLn)
	go serve(healthSrv, healthLn)

	cacheCtx, stopCache := context.WithCancel(context.WithoutCancel(ctx))
	defer stopCache()
	go func() {
		err := s.cache.Start(cacheCtx)
		switch {
		case err != nil:
			errc <- fmt.Errorf("api: cache: %w", err)
		case cacheCtx.Err() == nil:
			errc <- errors.New("api: cache stopped")
		}
	}()
	go func() {
		if s.cache.WaitForCacheSync(cacheCtx) {
			s.ready.Store(true)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}

	s.ready.Store(false)
	s.closeStreams()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	errs := []error{runErr}
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("api: shutdown %s: %w", apiLn.Addr(), err), apiSrv.Close())
	}
	if err := healthSrv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("api: shutdown %s: %w", healthLn.Addr(), err), healthSrv.Close())
	}
	return errors.Join(errs...)
}
