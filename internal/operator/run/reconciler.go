package run

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/operator/condition"
	"github.com/Devcon4/skua/internal/operator/patch"
)

const (
	// Finalizer holds an AgentRun until its key is revoked and its claim deleted.
	Finalizer = "skua.dev/cleanup"

	// ConditionKeyMinted is the AgentRun condition that reports the LiteLLM key mint.
	ConditionKeyMinted = "KeyMinted"
	// ConditionKeyRevoked is the AgentRun condition that reports the LiteLLM key revoke.
	ConditionKeyRevoked = "KeyRevoked"

	reasonMinted       = "Minted"
	reasonMintFailed   = "MintFailed"
	reasonRevoked      = "Revoked"
	reasonRevokeFailed = "RevokeFailed"
)

type reconciler struct {
	client client.Client
	scheme *runtime.Scheme
	deps   Deps
}

// Reconcile applies the run lifecycle; a status write refused because another writer already made the run
// terminal ends the pass, and the run's update event brings it back for cleanup.
func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	res, err := r.reconcile(ctx, req)
	if err == nil || (!apierrors.IsConflict(err) && !apierrors.IsInvalid(err)) {
		return res, err
	}
	var fresh v1alpha1.AgentRun
	if gerr := r.deps.APIReader.Get(ctx, req.NamespacedName, &fresh); gerr == nil && fresh.Status.Phase.Terminal() {
		return ctrl.Result{}, nil
	}
	return res, err
}

func (r *reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &v1alpha1.AgentRun{}
	if err := r.client.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, run)
	}
	if !controllerutil.ContainsFinalizer(run, Finalizer) {
		if err := patch.Object(ctx, r.client, run, func() { controllerutil.AddFinalizer(run, Finalizer) }); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
	}
	if run.Status.ProfileSnapshot == nil && !run.Status.Phase.Terminal() {
		if err := r.admit(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
	}
	if run.Status.Phase.Terminal() {
		return r.finish(ctx, run)
	}

	key, err := r.ensureSecret(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureKey(ctx, run, key); err != nil {
		return ctrl.Result{}, err
	}
	if run.Status.Phase.Terminal() {
		return r.finish(ctx, run)
	}
	if run.Status.Phase == v1alpha1.PhasePending {
		return r.ensureClaim(ctx, run)
	}
	return ctrl.Result{}, nil
}

func (r *reconciler) fail(ctx context.Context, run *v1alpha1.AgentRun, reason, msg string) error {
	return patch.FailRun(ctx, r.client, r.deps.APIReader, run, reason, msg, r.deps.Clock.Now())
}

func (r *reconciler) setCondition(run *v1alpha1.AgentRun, condType string, status metav1.ConditionStatus, reason, msg string) {
	condition.Set(&run.Status.Conditions, run.Generation, condType, status, reason, msg, r.deps.Clock.Now())
}

func keyAlias(run *v1alpha1.AgentRun) string {
	return "skua-" + string(run.UID)
}
