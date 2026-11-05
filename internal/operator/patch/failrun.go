package patch

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
)

// FailRun moves a non-terminal run to Failed with reason and message, setting finishedAt to now if unset.
// The AgentRun CEL rule makes the first terminal write win: a write refused because another writer already
// made the run terminal replaces *run with that fresh run and returns nil. A terminal run is left alone.
func FailRun(ctx context.Context, c client.Client, reader client.Reader, run *v1alpha1.AgentRun, reason, message string, now time.Time) error {
	if run.Status.Phase.Terminal() {
		return nil
	}
	err := Status(ctx, c, run, func() {
		run.Status.Phase = v1alpha1.PhaseFailed
		run.Status.Reason = reason
		run.Status.Message = message
		if run.Status.FinishedAt == nil {
			t := metav1.NewTime(now)
			run.Status.FinishedAt = &t
		}
	})
	if err == nil {
		log.FromContext(ctx).Info("run failed", "reason", reason, "message", message)
		return nil
	}
	if !apierrors.IsConflict(err) && !apierrors.IsInvalid(err) {
		return fmt.Errorf("fail run %s with %s: %w", run.Name, reason, err)
	}
	var fresh v1alpha1.AgentRun
	if getErr := reader.Get(ctx, client.ObjectKeyFromObject(run), &fresh); getErr != nil || !fresh.Status.Phase.Terminal() {
		return fmt.Errorf("fail run %s with %s: %w", run.Name, reason, err)
	}
	*run = fresh
	return nil
}
