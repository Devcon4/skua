package forge

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/operator/patch"
)

func (r *reconciler) revokeAll(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, Finalizer) {
		return ctrl.Result{}, nil
	}
	logger := log.FromContext(ctx)
	secret, err := r.runSecret(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if secret == nil {
		logger.Info("run has no forge token Secret; releasing the forge-tokens finalizer")
		return ctrl.Result{}, r.releaseFinalizer(ctx, run, nil)
	}
	live, err := parseLive(secret)
	if err != nil {
		logger.Error(err, "forge token list is unreadable; releasing the forge-tokens finalizer without revoking")
		return ctrl.Result{}, r.releaseFinalizer(ctx, run, secret)
	}

	var remaining []liveToken
	var errs []error
	for _, l := range live {
		if err := r.revoke(ctx, l.Token); err != nil {
			remaining = append(remaining, l)
			errs = append(errs, err)
		}
	}
	if len(live) > 0 {
		if err := r.storeLive(ctx, secret, remaining); err != nil {
			return ctrl.Result{}, err
		}
	}

	now := r.clock.Now()
	if len(errs) == 0 {
		if err := r.setCondition(ctx, run, metav1.ConditionFalse, reasonRevoked, "forge tokens revoked", now); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.releaseFinalizer(ctx, run, secret)
	}

	revokeErr := fmt.Errorf("revoke forge tokens: %w", errors.Join(errs...))
	start := now
	if c := meta.FindStatusCondition(run.Status.Conditions, ConditionType); c != nil && c.Status == metav1.ConditionFalse && c.Reason == reasonRevokeFailed {
		start = c.LastTransitionTime.Time
	}
	if now.Sub(start) >= revokeGrace {
		logger.Error(revokeErr, "releasing the forge-tokens finalizer after revocation kept failing", "for", revokeGrace)
		return ctrl.Result{}, r.releaseFinalizer(ctx, run, secret)
	}
	if err := r.setCondition(ctx, run, metav1.ConditionFalse, reasonRevokeFailed, revokeErr.Error(), start); err != nil {
		return ctrl.Result{}, errors.Join(revokeErr, err)
	}
	return ctrl.Result{}, revokeErr
}

func (r *reconciler) revoke(ctx context.Context, token string) error {
	if r.forge == nil {
		return errors.New("installer config has no github section")
	}
	ctx, cancel := context.WithTimeout(ctx, r.revokeAttempt)
	defer cancel()
	return r.forge.Revoke(ctx, token)
}

// The Secret's finalizer goes first so a Secret deleted ahead of the run still holds its tokens for revocation.
func (r *reconciler) releaseFinalizer(ctx context.Context, run *v1alpha1.AgentRun, secret *corev1.Secret) error {
	if secret != nil {
		if err := patch.Object(ctx, r.client, secret, func() { controllerutil.RemoveFinalizer(secret, Finalizer) }); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("release forge-tokens finalizer of Secret %s: %w", secret.Name, err)
		}
	}
	return client.IgnoreNotFound(patch.Object(ctx, r.client, run, func() { controllerutil.RemoveFinalizer(run, Finalizer) }))
}
