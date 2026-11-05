// Package health fails non-terminal AgentRuns that exceed MaxRuntime, crash-loop or lose their pod.
package health

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
)

// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,namespace=skua-sandboxes,resources=sandboxclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",namespace=skua-sandboxes,resources=pods,verbs=get;list;watch

// Deps are what the run health reconciler needs. APIReader is the manager's uncached reader.
type Deps struct {
	Config    *config.Config
	APIReader client.Reader
	Clock     clock.PassiveClock
}

// Setup registers the controller that fails non-terminal runs with MaxRuntime, CrashLoop or PodLost.
// It errors when d lacks Config, APIReader or Clock.
func Setup(mgr ctrl.Manager, d Deps) error {
	if d.Config == nil || d.APIReader == nil || d.Clock == nil {
		return errors.New("run health reconciler: deps need Config, APIReader and Clock")
	}
	r := &reconciler{
		client:    mgr.GetClient(),
		apiReader: d.APIReader,
		cfg:       d.Config,
		clock:     d.Clock,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("runhealth").
		For(&v1alpha1.AgentRun{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToRun)).
		Complete(r)
}
