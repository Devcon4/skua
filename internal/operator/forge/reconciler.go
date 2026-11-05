package forge

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/integration/github"
	"github.com/Devcon4/skua/internal/operator/condition"
	"github.com/Devcon4/skua/internal/operator/patch"
)

const (
	// Finalizer holds a forge run and its Secret until every live token is revoked.
	Finalizer = "skua.dev/forge-tokens"
	// ConditionType is the AgentRun condition that reports the forge token's state.
	ConditionType = "ForgeToken"
	// ReasonRateLimited is the ConditionType reason while GitHub rate-limits the token mint.
	ReasonRateLimited = "RateLimited"

	reasonMinted       = "Minted"
	reasonMintFailed   = "MintFailed"
	reasonRevokeFailed = "RevokeFailed"
	reasonRevoked      = "Revoked"

	mintDeadline = 5 * time.Minute
	revokeGrace  = 2 * time.Minute
)

type reconciler struct {
	client        client.Client
	reader        client.Reader
	cfg           *config.Config
	forge         github.Minter
	clock         clock.PassiveClock
	revokeAttempt time.Duration
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &v1alpha1.AgentRun{}
	if err := r.client.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() || run.Status.Phase.Terminal() {
		return r.revokeAll(ctx, run)
	}
	snap := run.Status.ProfileSnapshot
	if snap == nil || snap.Forge != v1alpha1.ForgeGitHub || run.Spec.Workspace == nil {
		return ctrl.Result{}, nil
	}
	if r.cfg.GitHub == nil || r.forge == nil {
		return ctrl.Result{}, r.failRun(ctx, run, "profile uses forge github but the installer config has no github section")
	}
	if !controllerutil.ContainsFinalizer(run, Finalizer) {
		return ctrl.Result{}, patch.Object(ctx, r.client, run, func() { controllerutil.AddFinalizer(run, Finalizer) })
	}
	return r.ensureToken(ctx, run)
}

func (r *reconciler) failRun(ctx context.Context, run *v1alpha1.AgentRun, message string) error {
	return patch.FailRun(ctx, r.client, r.reader, run, v1alpha1.ReasonForgeError, message, r.clock.Now())
}

func (r *reconciler) setCondition(ctx context.Context, run *v1alpha1.AgentRun, status metav1.ConditionStatus, reason, message string, since time.Time) error {
	return patch.Status(ctx, r.client, run, func() {
		condition.Set(&run.Status.Conditions, run.Generation, ConditionType, status, reason, message, since)
	})
}
