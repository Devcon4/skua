// Package forge keeps a repository-scoped GitHub token in each forge run's Secret and revokes every live
// token when the run ends.
package forge

import (
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/integration/github"
)

// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",namespace=skua,resources=secrets,verbs=get;list;watch;update;patch

// Deps are what the forge reconciler needs. Forge is nil when config.GitHub is unset; APIReader is the
// manager's uncached reader.
type Deps struct {
	Config    *config.Config
	Forge     github.Minter
	APIReader client.Reader
	Clock     clock.PassiveClock
	Timing    Timing
}

// Timing holds the forge reconciler's real-time intervals; a zero field takes its production default.
type Timing struct {
	// RevokeAttempt bounds one forge token revoke call; default 30s.
	RevokeAttempt time.Duration
	// RetryBase and RetryMax bound the per-run exponential backoff after a failed reconcile; defaults 5ms and 30s.
	RetryBase time.Duration
	RetryMax  time.Duration
}

func (t Timing) withDefaults() Timing {
	for _, f := range []struct {
		v   *time.Duration
		def time.Duration
	}{
		{&t.RevokeAttempt, 30 * time.Second},
		{&t.RetryBase, 5 * time.Millisecond},
		{&t.RetryMax, 30 * time.Second},
	} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	return t
}

// Setup registers the controller that keeps a repository-scoped GitHub token in each forge run's Secret,
// re-mints it at half its lifetime and revokes every live token when the run ends.
func Setup(mgr ctrl.Manager, d Deps) error {
	if d.Config == nil || d.APIReader == nil || d.Clock == nil {
		return errors.New("forge reconciler: deps need Config, APIReader and Clock")
	}
	timing := d.Timing.withDefaults()
	r := &reconciler{
		client:        mgr.GetClient(),
		reader:        d.APIReader,
		cfg:           d.Config,
		forge:         d.Forge,
		clock:         d.Clock,
		revokeAttempt: timing.RevokeAttempt,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("forge").
		For(&v1alpha1.AgentRun{}).
		Owns(&corev1.Secret{}, builder.MatchEveryOwner).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](timing.RetryBase, timing.RetryMax),
		}).
		Complete(r)
}
