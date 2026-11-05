// Package run reconciles AgentRuns: it admits each run against its profile, mints the run's LiteLLM key,
// claims a sandbox, and on a terminal phase or deletion revokes the key, deletes the claim and applies
// retention.
package run

import (
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/integration/litellm"
)

// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=skua.dev,resources=agentprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups="",namespace=skua,resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,namespace=skua-sandboxes,resources=sandboxclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.x-k8s.io,namespace=skua-sandboxes,resources=sandboxes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",namespace=skua-sandboxes,resources=pods,verbs=get;list;watch

// Deps are what the run reconciler needs. APIReader is the manager's uncached reader.
type Deps struct {
	Config    *config.Config
	Gateway   litellm.Provider
	APIReader client.Reader
	Clock     clock.PassiveClock
	Timing    Timing
}

// Timing holds the run reconciler's real-time intervals; a zero field takes its production default.
type Timing struct {
	// RevokeAttempt bounds one LiteLLM key revoke call; default 30s.
	RevokeAttempt time.Duration
	// RetryBase and RetryMax bound the per-run exponential backoff after a failed reconcile; defaults 5ms and 30s.
	RetryBase time.Duration
	RetryMax  time.Duration
	// PodPoll is how often a run whose claim is bound but whose pod is not yet visible is reconciled; default 1s.
	PodPoll time.Duration
}

func (t Timing) withDefaults() Timing {
	for _, f := range []struct {
		v   *time.Duration
		def time.Duration
	}{
		{&t.RevokeAttempt, 30 * time.Second},
		{&t.RetryBase, 5 * time.Millisecond},
		{&t.RetryMax, 30 * time.Second},
		{&t.PodPoll, time.Second},
	} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	return t
}

// Setup registers the AgentRun reconciler on mgr. It errors when d lacks Config, Gateway, APIReader or Clock.
func Setup(mgr ctrl.Manager, d Deps) error {
	switch {
	case d.Config == nil:
		return errors.New("run reconciler: config is nil")
	case d.Gateway == nil:
		return errors.New("run reconciler: gateway provider is nil")
	case d.APIReader == nil:
		return errors.New("run reconciler: API reader is nil")
	case d.Clock == nil:
		return errors.New("run reconciler: clock is nil")
	}
	d.Timing = d.Timing.withDefaults()
	r := &reconciler{
		client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
		deps:   d,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("agentrun").
		For(&v1alpha1.AgentRun{}).
		Owns(&corev1.Secret{}).
		Watches(&extv1beta1.SandboxClaim{}, handler.EnqueueRequestsFromMapFunc(r.claimToRun)).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](d.Timing.RetryBase, d.Timing.RetryMax),
		}).
		Complete(r)
}
