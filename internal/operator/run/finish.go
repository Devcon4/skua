package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/operator/condition"
	"github.com/Devcon4/skua/internal/operator/patch"
)

const revokeDeadline = 2 * time.Minute

// A failing revoke never blocks claim deletion or finishedAt, and stops holding back retention after revokeDeadline.
func (r *reconciler) finish(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	var revokeErr error
	if !meta.IsStatusConditionTrue(run.Status.Conditions, ConditionKeyRevoked) {
		revokeErr = r.revoke(ctx, run)
	}
	if err := patch.Status(ctx, r.client, run, func() {
		if run.Status.FinishedAt == nil {
			now := metav1.NewTime(r.deps.Clock.Now())
			run.Status.FinishedAt = &now
		}
	}); err != nil {
		return ctrl.Result{}, errors.Join(revokeErr, err)
	}
	if err := r.deleteClaim(ctx, run); err != nil {
		return ctrl.Result{}, errors.Join(revokeErr, err)
	}
	if revokeErr != nil && !r.revokeExpired(ctx, run, revokeErr) {
		return ctrl.Result{}, revokeErr
	}
	return r.retain(ctx, run)
}

func (r *reconciler) finalize(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, Finalizer) {
		return ctrl.Result{}, nil
	}
	if !meta.IsStatusConditionTrue(run.Status.Conditions, ConditionKeyRevoked) {
		if err := r.revoke(ctx, run); err != nil && !r.revokeExpired(ctx, run, err) {
			return ctrl.Result{}, err
		}
	}
	if err := r.deleteClaim(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	err := patch.Object(ctx, r.client, run, func() { controllerutil.RemoveFinalizer(run, Finalizer) })
	return ctrl.Result{}, client.IgnoreNotFound(err)
}

func (r *reconciler) revokeExpired(ctx context.Context, run *v1alpha1.AgentRun, revokeErr error) bool {
	since, failing := condition.FailingSince(run.Status.Conditions, ConditionKeyRevoked)
	if !failing || r.deps.Clock.Since(since) < revokeDeadline {
		return false
	}
	ctrl.LoggerFrom(ctx).Error(revokeErr, "cleaning up after revoke kept failing", "alias", keyAlias(run), "since", since)
	return true
}

func (r *reconciler) revoke(ctx context.Context, run *v1alpha1.AgentRun) error {
	attempt, cancel := context.WithTimeout(ctx, r.deps.Timing.RevokeAttempt)
	err := r.deps.Gateway.Revoke(attempt, keyAlias(run))
	cancel()
	status, reason, msg := metav1.ConditionTrue, reasonRevoked, ""
	if err != nil {
		status, reason, msg = metav1.ConditionFalse, reasonRevokeFailed, err.Error()
	}
	if perr := patch.Status(ctx, r.client, run, func() { r.setCondition(run, ConditionKeyRevoked, status, reason, msg) }); perr != nil {
		return perr
	}
	if err != nil {
		return fmt.Errorf("revoke key %s: %w", keyAlias(run), err)
	}
	return nil
}

func (r *reconciler) deleteClaim(ctx context.Context, run *v1alpha1.AgentRun) error {
	var claim extv1beta1.SandboxClaim
	key := client.ObjectKey{Namespace: r.deps.Config.SandboxNamespace, Name: run.Name}
	err := r.deps.APIReader.Get(ctx, key, &claim)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get claim %s: %w", key.Name, err)
	}
	if claim.Labels[sandbox.LabelRunUID] != string(run.UID) || !claim.DeletionTimestamp.IsZero() {
		return nil
	}
	if err := r.client.Delete(ctx, &claim, client.Preconditions{UID: &claim.UID}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete claim %s: %w", key.Name, err)
	}
	return nil
}
