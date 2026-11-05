package agentrun

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
)

// +kubebuilder:rbac:groups=skua.dev,namespace=skua,resources=agentruns/status,verbs=get;update;patch

// Finish applies apply to run's status and sets FinishedAt when apply left it unset, unless the run is already
// terminal. On a conflict or a CEL refusal it rereads the run through fresh and retries; a run that is gone or
// was recreated under another UID is left alone.
func Finish(ctx context.Context, c client.Client, fresh client.Reader, clk clock.PassiveClock, run *v1alpha1.AgentRun, apply func(*v1alpha1.AgentRunStatus)) error {
	cur, stale := run, false
	err := retry.OnError(retry.DefaultRetry, conflictOrInvalid, func() error {
		if stale {
			var latest v1alpha1.AgentRun
			if err := fresh.Get(ctx, client.ObjectKeyFromObject(run), &latest); err != nil {
				return client.IgnoreNotFound(err)
			}
			if latest.UID != run.UID {
				return nil
			}
			cur = &latest
		}
		if cur.Status.Phase.Terminal() {
			return nil
		}
		next := cur.DeepCopy()
		apply(&next.Status)
		if next.Status.FinishedAt == nil {
			now := metav1.NewTime(clk.Now())
			next.Status.FinishedAt = &now
		}
		stale = true
		return c.Status().Update(ctx, next)
	})
	if err != nil {
		return fmt.Errorf("update status of run %s: %w", run.Name, err)
	}
	return nil
}

// conflictOrInvalid also retries a CEL refusal, which means another writer made the run terminal first.
func conflictOrInvalid(err error) bool {
	return apierrors.IsConflict(err) || apierrors.IsInvalid(err)
}
