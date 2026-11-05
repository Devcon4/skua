// Package operator wires skua-operator: it builds the controller manager and registers the profile, run,
// health and forge reconcilers on it.
package operator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/integration/github"
	"github.com/Devcon4/skua/internal/integration/kube"
	"github.com/Devcon4/skua/internal/integration/litellm"
	"github.com/Devcon4/skua/internal/operator/compat"
	"github.com/Devcon4/skua/internal/operator/forge"
	"github.com/Devcon4/skua/internal/operator/health"
	"github.com/Devcon4/skua/internal/operator/profile"
	"github.com/Devcon4/skua/internal/operator/run"
)

// +kubebuilder:rbac:groups=coordination.k8s.io,namespace=skua,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="";events.k8s.io,namespace=skua,resources=events,verbs=create;patch

const (
	managerMetricsAddr        = ":8080"
	managerHealthAddr         = ":8081"
	managerLeaderElectionID   = "skua-operator.skua.dev"
	managerHTTPTimeout        = 30 * time.Second
	managerSandboxCompatEvery = 5 * time.Minute
)

// Deps are the integrations Register hands to the reconcilers.
// Forge is nil when config.GitHub is unset; APIReader is the manager's uncached reader; SandboxCompat is nil
// when no agent-sandbox version check runs.
type Deps struct {
	Config        *config.Config
	Gateway       litellm.Provider
	Forge         github.Minter
	APIReader     client.Reader
	Clock         clock.PassiveClock
	SandboxCompat profile.Compat
	Timing        Timing
}

// Timing holds the reconcilers' real-time intervals; a zero field takes its production default.
type Timing struct {
	// RevokeAttempt bounds one LiteLLM key or forge token revoke call; default 30s.
	RevokeAttempt time.Duration
	// RetryBase and RetryMax bound the per-run exponential backoff after a failed reconcile; defaults 5ms and 30s.
	RetryBase time.Duration
	RetryMax  time.Duration
	// PodPoll is how often a run whose claim is bound but whose pod is not yet visible is reconciled; default 1s.
	PodPoll time.Duration
	// ProfileRecheck is how often an AgentProfile is reconciled to refresh its gateway checks; default 60s.
	ProfileRecheck time.Duration
	// PoolStartWindow is how long, on Deps.Clock, a warm pool may have no ready replica before its
	// profile reports PoolNotStarting; default 5m.
	PoolStartWindow time.Duration
}

// Run starts skua-operator against the cluster of the ambient kubeconfig and blocks until ctx ends or the
// manager fails. It errors when the LiteLLM operator key Secret cannot be read.
func Run(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return errors.New("operator: config is nil")
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("operator: kubeconfig: %w", err)
	}
	scheme, err := kube.Scheme()
	if err != nil {
		return fmt.Errorf("operator: scheme: %w", err)
	}
	mgr, err := ctrl.NewManager(restCfg, managerOptions(cfg, scheme))
	if err != nil {
		return fmt.Errorf("operator: create manager: %w", err)
	}

	operatorKey, err := gatewayKey(ctx, mgr.GetAPIReader(), cfg)
	if err != nil {
		return fmt.Errorf("operator: %w", err)
	}
	hc := &http.Client{Timeout: managerHTTPTimeout}
	compatCheck := compat.NewCheck(mgr.GetAPIReader(), cfg.AgentSandbox.Namespace, managerSandboxCompatEvery)
	if err := mgr.Add(compatCheck); err != nil {
		return fmt.Errorf("operator: add agent-sandbox version check: %w", err)
	}
	deps := Deps{
		Config:        cfg,
		Gateway:       litellm.New(cfg.Gateway.BaseURL, operatorKey, hc),
		APIReader:     mgr.GetAPIReader(),
		Clock:         clock.RealClock{},
		SandboxCompat: compatCheck,
	}
	if cfg.GitHub != nil {
		deps.Forge = github.NewMinter(mgr.GetAPIReader(), *cfg.GitHub, hc, deps.Clock)
	}
	if err := Register(mgr, deps); err != nil {
		return fmt.Errorf("operator: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("operator: health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("operator: ready check: %w", err)
	}
	return mgr.Start(ctx)
}

// Register sets up the profile, run, health and forge reconcilers on mgr, each with its share of d.
func Register(mgr ctrl.Manager, d Deps) error {
	setups := []struct {
		name  string
		setup func() error
	}{
		{"profile", func() error {
			return profile.Setup(mgr, profile.Deps{
				Config:  d.Config,
				Gateway: d.Gateway,
				Clock:   d.Clock,
				Compat:  d.SandboxCompat,
				Timing:  profile.Timing{Recheck: d.Timing.ProfileRecheck, PoolStartWindow: d.Timing.PoolStartWindow},
			})
		}},
		{"run", func() error {
			return run.Setup(mgr, run.Deps{
				Config:    d.Config,
				Gateway:   d.Gateway,
				APIReader: d.APIReader,
				Clock:     d.Clock,
				Timing: run.Timing{
					RevokeAttempt: d.Timing.RevokeAttempt,
					RetryBase:     d.Timing.RetryBase,
					RetryMax:      d.Timing.RetryMax,
					PodPoll:       d.Timing.PodPoll,
				},
			})
		}},
		{"run health", func() error {
			return health.Setup(mgr, health.Deps{Config: d.Config, APIReader: d.APIReader, Clock: d.Clock})
		}},
		{"forge", func() error {
			return forge.Setup(mgr, forge.Deps{
				Config:    d.Config,
				Forge:     d.Forge,
				APIReader: d.APIReader,
				Clock:     d.Clock,
				Timing:    forge.Timing{RevokeAttempt: d.Timing.RevokeAttempt, RetryBase: d.Timing.RetryBase, RetryMax: d.Timing.RetryMax},
			})
		}},
	}
	for _, s := range setups {
		if err := s.setup(); err != nil {
			return fmt.Errorf("set up %s reconciler: %w", s.name, err)
		}
	}
	return nil
}

func managerOptions(cfg *config.Config, scheme *runtime.Scheme) ctrl.Options {
	return ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: managerMetricsAddr},
		HealthProbeBindAddress:  managerHealthAddr,
		LeaderElection:          true,
		LeaderElectionID:        managerLeaderElectionID,
		LeaderElectionNamespace: cfg.RunNamespace,
		Cache:                   CacheOptions(cfg),
	}
}
