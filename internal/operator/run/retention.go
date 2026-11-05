package run

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
)

func (r *reconciler) retain(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	deadline := r.retentionDeadline(ctx, run)
	if now := r.deps.Clock.Now(); now.Before(deadline) {
		return ctrl.Result{RequeueAfter: deadline.Sub(now)}, nil
	}
	if err := r.client.Delete(ctx, run, client.Preconditions{UID: &run.UID}); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("delete run after retention: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *reconciler) retentionDeadline(ctx context.Context, run *v1alpha1.AgentRun) time.Time {
	retention := r.deps.Config.Retention
	deadline := run.Status.FinishedAt.Add(retention.Max.Duration)
	v, fetchedKnown := run.Annotations[v1alpha1.AnnotationFetchedAt]
	if !fetchedKnown {
		return deadline
	}
	fetched, err := time.Parse(time.RFC3339, v)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "ignoring unparsable annotation", "annotation", v1alpha1.AnnotationFetchedAt)
		return deadline
	}
	if d := fetched.Add(retention.AfterFetch.Duration); d.Before(deadline) {
		return d
	}
	return deadline
}
