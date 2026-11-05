// Package profile reconciles each AgentProfile into the agent-sandbox SandboxTemplate and SandboxWarmPool
// that back it, and reports whether the profile is Ready.
package profile

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/integration/litellm"
)

// +kubebuilder:rbac:groups=skua.dev,resources=agentprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=skua.dev,resources=agentprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=skua.dev,resources=agentprofiles/finalizers,verbs=update
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,namespace=skua-sandboxes,resources=sandboxtemplates;sandboxwarmpools,verbs=get;list;watch;create;update;patch;delete

// Compat is the agent-sandbox version check. Unsupported returns the latest result's message and true when
// the installed agent-sandbox is unsupported; Events receives one event after each change of the result.
type Compat interface {
	Unsupported() (message string, unsupported bool)
	Events() <-chan event.TypedGenericEvent[struct{}]
}

// Deps are what the profile reconciler needs. Compat is nil when no agent-sandbox version check runs.
type Deps struct {
	Config  *config.Config
	Gateway litellm.Provider
	Clock   clock.PassiveClock
	Compat  Compat
	Timing  Timing
}

// Timing holds the profile reconciler's real-time intervals; a zero field takes its production default.
type Timing struct {
	// Recheck is how often an AgentProfile is reconciled to refresh its gateway checks; default 60s.
	Recheck time.Duration
	// PoolStartWindow is how long, on Deps.Clock, a warm pool may have no ready replica before its
	// profile reports PoolNotStarting; default 5m.
	PoolStartWindow time.Duration
}

func (t Timing) withDefaults() Timing {
	for _, f := range []struct {
		v   *time.Duration
		def time.Duration
	}{
		{&t.Recheck, 60 * time.Second},
		{&t.PoolStartWindow, 5 * time.Minute},
	} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	return t
}

// Setup registers the AgentProfile reconciler on mgr. It errors when mgr is nil or d lacks Config, Gateway
// or Clock.
func Setup(mgr ctrl.Manager, d Deps) error {
	switch {
	case mgr == nil:
		return errors.New("profile reconciler: manager is nil")
	case d.Config == nil:
		return errors.New("profile reconciler: deps.Config is nil")
	case d.Gateway == nil:
		return errors.New("profile reconciler: deps.Gateway is nil")
	case d.Clock == nil:
		return errors.New("profile reconciler: deps.Clock is nil")
	}
	r := &reconciler{
		client:  mgr.GetClient(),
		cfg:     d.Config,
		gateway: d.Gateway,
		clock:   d.Clock,
		compat:  d.Compat,
		timing:  d.Timing.withDefaults(),
		pools:   map[string]poolState{},
	}
	b := ctrl.NewControllerManagedBy(mgr).
		Named("agentprofile").
		For(&v1alpha1.AgentProfile{}).
		Owns(&extv1beta1.SandboxTemplate{}).
		Owns(&extv1beta1.SandboxWarmPool{})
	if r.compat != nil {
		b = b.WatchesRawSource(source.Channel(r.compat.Events(), handler.TypedEnqueueRequestsFromMapFunc(r.allProfiles)))
	}
	return b.Complete(r)
}

func (r *reconciler) allProfiles(ctx context.Context, _ struct{}) []reconcile.Request {
	var profiles v1alpha1.AgentProfileList
	if err := r.client.List(ctx, &profiles); err != nil {
		log.FromContext(ctx).Error(err, "list AgentProfiles after an agent-sandbox version change")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(profiles.Items))
	for _, p := range profiles.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: p.Name}})
	}
	return reqs
}
