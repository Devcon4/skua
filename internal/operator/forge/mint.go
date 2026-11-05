package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/integration/github"
	"github.com/Devcon4/skua/internal/operator/patch"
)

func (r *reconciler) ensureToken(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	secret, err := r.runSecret(ctx, run)
	if err != nil || secret == nil {
		return ctrl.Result{}, err
	}
	live, err := parseLive(secret)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := r.clock.Now()
	current, hasCurrent := parseCurrent(secret)
	if hasCurrent && !github.RemintDue(current, now) {
		return ctrl.Result{RequeueAfter: remintAt(current).Sub(now)}, nil
	}

	tok, mintErr := r.forge.Mint(ctx, run.Spec.Workspace.Repo)
	if mintErr != nil {
		return r.mintFailed(ctx, run, current, hasCurrent, now, mintErr)
	}
	if err := r.storeToken(ctx, secret, live, tok, now); err != nil {
		if rerr := r.forge.Revoke(ctx, tok.Value); rerr != nil {
			log.FromContext(ctx).Error(rerr, "revoke forge token that could not be stored")
		}
		return ctrl.Result{}, err
	}
	if err := r.setCondition(ctx, run, metav1.ConditionTrue, reasonMinted, "forge token minted", now); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: remintAt(tok).Sub(now)}, nil
}

func (r *reconciler) mintFailed(ctx context.Context, run *v1alpha1.AgentRun, current github.Token, hasCurrent bool, now time.Time, mintErr error) (ctrl.Result, error) {
	start := now
	if c := meta.FindStatusCondition(run.Status.Conditions, ConditionType); c != nil && c.Status == metav1.ConditionFalse &&
		(c.Reason == reasonMintFailed || c.Reason == ReasonRateLimited) {
		start = c.LastTransitionTime.Time
	}
	failAt := start.Add(mintDeadline)
	if hasCurrent && current.ExpiresAt.After(failAt) {
		failAt = current.ExpiresAt
	}
	if !now.Before(failAt) {
		return ctrl.Result{}, r.failRun(ctx, run, fmt.Sprintf("no forge token %s after the first mint attempt: %v", mintDeadline, mintErr))
	}

	var rl *github.RateLimitError
	if errors.As(mintErr, &rl) {
		if err := r.setCondition(ctx, run, metav1.ConditionFalse, ReasonRateLimited, "GitHub rate limited the token mint", start); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: min(rl.RetryAfter, failAt.Sub(now))}, nil
	}
	if err := r.setCondition(ctx, run, metav1.ConditionFalse, reasonMintFailed, mintErr.Error(), start); err != nil {
		return ctrl.Result{}, errors.Join(mintErr, err)
	}
	return ctrl.Result{}, fmt.Errorf("mint forge token: %w", mintErr)
}

func (r *reconciler) storeToken(ctx context.Context, secret *corev1.Secret, live []liveToken, tok github.Token, now time.Time) error {
	kept := make([]liveToken, 0, len(live)+1)
	for _, l := range live {
		if now.Before(l.ExpiresAt) {
			kept = append(kept, l)
		}
	}
	kept = append(kept, liveToken{Token: tok.Value, ExpiresAt: tok.ExpiresAt.UTC()})
	liveJSON, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("encode %s: %w", SecretKeyTokensLive, err)
	}

	return patch.Object(ctx, r.client, secret, func() {
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[sandbox.SecretKeyForgeToken] = []byte(tok.Value)
		secret.Data[sandbox.SecretKeyForgeExpiresAt] = []byte(tok.ExpiresAt.UTC().Format(time.RFC3339))
		secret.Data[secretKeyIssuedAt] = []byte(tok.IssuedAt.UTC().Format(time.RFC3339))
		secret.Data[SecretKeyTokensLive] = liveJSON
		controllerutil.AddFinalizer(secret, Finalizer)
	})
}

func remintAt(t github.Token) time.Time {
	return t.IssuedAt.Add(t.ExpiresAt.Sub(t.IssuedAt) / 2)
}
