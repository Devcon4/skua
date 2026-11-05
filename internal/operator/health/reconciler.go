package health

import (
	"context"
	"fmt"

	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/operator/patch"
)

type reconciler struct {
	client    client.Client
	apiReader client.Reader
	cfg       *config.Config
	clock     clock.PassiveClock
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run v1alpha1.AgentRun
	if err := r.client.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	snapshot := run.Status.ProfileSnapshot
	if run.Status.Phase.Terminal() || snapshot == nil {
		return ctrl.Result{}, nil
	}

	var result ctrl.Result
	if maxRuntime := snapshot.Limits.MaxRuntime.Duration; maxRuntime > 0 {
		deadline := run.CreationTimestamp.Add(maxRuntime)
		now := r.clock.Now()
		if !now.Before(deadline) {
			return ctrl.Result{}, r.fail(ctx, &run, v1alpha1.ReasonMaxRuntime,
				fmt.Sprintf("run exceeded maxRuntime %s", maxRuntime))
		}
		result.RequeueAfter = deadline.Sub(now)
	}

	if run.Status.Phase != v1alpha1.PhaseRunning || run.Status.PodName == "" {
		return result, nil
	}
	reason, message, err := r.podFailure(ctx, &run, snapshot.Limits)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason == "" {
		return result, nil
	}
	return ctrl.Result{}, r.fail(ctx, &run, reason, message)
}

func (r *reconciler) fail(ctx context.Context, run *v1alpha1.AgentRun, reason, message string) error {
	return patch.FailRun(ctx, r.client, r.apiReader, run, reason, message, r.clock.Now())
}
