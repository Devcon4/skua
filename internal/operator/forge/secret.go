package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/integration/github"
	"github.com/Devcon4/skua/internal/operator/patch"
)

const (
	// SecretKeyTokensLive is the run Secret key holding the JSON list of tokens not yet revoked, each
	// {"token": ..., "expiresAt": ...}.
	SecretKeyTokensLive = "forgeTokensLive"

	secretKeyIssuedAt = "forgeIssuedAt"
)

type liveToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (r *reconciler) runSecret(ctx context.Context, run *v1alpha1.AgentRun) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: run.Namespace, Name: sandbox.SecretName(run.Name)}
	// Uncached so a token stored by the previous pass is never minted a second time from a stale cache.
	if err := r.reader.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get run Secret %s: %w", key.Name, err)
	}
	for _, ref := range secret.OwnerReferences {
		if ref.UID == run.UID {
			return secret, nil
		}
	}
	// Deleting with --cascade=orphan strips the ownerReference before this pass; the finalizer still marks our tokens.
	if !run.DeletionTimestamp.IsZero() && metav1.GetControllerOf(secret) == nil &&
		secret.Labels[sandbox.LabelRunName] == run.Name && controllerutil.ContainsFinalizer(secret, Finalizer) {
		return secret, nil
	}
	return nil, nil
}

func (r *reconciler) storeLive(ctx context.Context, secret *corev1.Secret, live []liveToken) error {
	if live == nil {
		live = []liveToken{}
	}
	liveJSON, err := json.Marshal(live)
	if err != nil {
		return fmt.Errorf("encode %s: %w", SecretKeyTokensLive, err)
	}
	return patch.Object(ctx, r.client, secret, func() { secret.Data[SecretKeyTokensLive] = liveJSON })
}

func parseCurrent(secret *corev1.Secret) (github.Token, bool) {
	value := secret.Data[sandbox.SecretKeyForgeToken]
	if len(value) == 0 {
		return github.Token{}, false
	}
	expiresAt, err := time.Parse(time.RFC3339, string(secret.Data[sandbox.SecretKeyForgeExpiresAt]))
	if err != nil {
		return github.Token{}, false
	}
	issuedAt, err := time.Parse(time.RFC3339, string(secret.Data[secretKeyIssuedAt]))
	if err != nil {
		return github.Token{}, false
	}
	return github.Token{Value: string(value), ExpiresAt: expiresAt, IssuedAt: issuedAt}, true
}

func parseLive(secret *corev1.Secret) ([]liveToken, error) {
	raw := secret.Data[SecretKeyTokensLive]
	if len(raw) == 0 {
		return nil, nil
	}
	var live []liveToken
	if err := json.Unmarshal(raw, &live); err != nil {
		return nil, fmt.Errorf("decode %s of Secret %s: %w", SecretKeyTokensLive, secret.Name, err)
	}
	return live, nil
}
