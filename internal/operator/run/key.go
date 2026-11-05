package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/integration/litellm"
	"github.com/Devcon4/skua/internal/operator/condition"
	"github.com/Devcon4/skua/internal/operator/patch"
)

const (
	// KeyValuePrefix starts every LiteLLM key value the operator generates; 32 hex characters follow it.
	KeyValuePrefix = "sk-skua-"

	keyGrace     = 5 * time.Minute
	mintDeadline = 5 * time.Minute
)

func (r *reconciler) ensureSecret(ctx context.Context, run *v1alpha1.AgentRun) (string, error) {
	key := client.ObjectKey{Namespace: run.Namespace, Name: sandbox.SecretName(run.Name)}
	var secret corev1.Secret
	err := r.client.Get(ctx, key, &secret)
	if apierrors.IsNotFound(err) {
		return r.createSecret(ctx, run, key)
	}
	if err != nil {
		return "", fmt.Errorf("get secret %s: %w", key.Name, err)
	}
	return secretKey(run, &secret)
}

func (r *reconciler) createSecret(ctx context.Context, run *v1alpha1.AgentRun, key client.ObjectKey) (string, error) {
	value, err := newKeyValue()
	if err != nil {
		return "", err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels:    map[string]string{sandbox.LabelRunSecret: "true", sandbox.LabelRunName: run.Name},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{sandbox.SecretKeyLiteLLM: []byte(value)},
	}
	if err := controllerutil.SetControllerReference(run, secret, r.scheme); err != nil {
		return "", fmt.Errorf("own secret %s: %w", key.Name, err)
	}
	err = r.client.Create(ctx, secret)
	if err == nil {
		return value, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create secret %s: %w", key.Name, err)
	}
	var existing corev1.Secret
	if err := r.deps.APIReader.Get(ctx, key, &existing); err != nil {
		return "", fmt.Errorf("get secret %s: %w", key.Name, err)
	}
	return secretKey(run, &existing)
}

func secretKey(run *v1alpha1.AgentRun, secret *corev1.Secret) (string, error) {
	if !metav1.IsControlledBy(secret, run) {
		return "", fmt.Errorf("secret %s is not controlled by run %s (uid %s)", secret.Name, run.Name, run.UID)
	}
	value := string(secret.Data[sandbox.SecretKeyLiteLLM])
	if value == "" {
		return "", fmt.Errorf("secret %s has no %s", secret.Name, sandbox.SecretKeyLiteLLM)
	}
	return value, nil
}

func newKeyValue() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key value: %w", err)
	}
	return KeyValuePrefix + hex.EncodeToString(b), nil
}

func (r *reconciler) ensureKey(ctx context.Context, run *v1alpha1.AgentRun, value string) error {
	if meta.IsStatusConditionTrue(run.Status.Conditions, ConditionKeyMinted) {
		return nil
	}
	snap := run.Status.ProfileSnapshot
	err := r.deps.Gateway.Mint(ctx, litellm.MintRequest{
		Key:       value,
		Alias:     keyAlias(run),
		Models:    snap.Models,
		MaxBudget: snap.Limits.BudgetUSD.AsApproximateFloat64(),
		Duration:  snap.Limits.MaxRuntime.Duration + keyGrace,
		Metadata: map[string]string{
			"run":       run.Name,
			"namespace": run.Namespace,
			"profile":   run.Spec.Profile,
			"owner":     run.Labels[v1alpha1.LabelOwner],
		},
	})
	if err == nil {
		return patch.Status(ctx, r.client, run, func() {
			r.setCondition(run, ConditionKeyMinted, metav1.ConditionTrue, reasonMinted, "")
		})
	}
	if errors.Is(err, litellm.ErrAliasConflict) {
		return r.fail(ctx, run, v1alpha1.ReasonGatewayError, err.Error())
	}
	now := r.deps.Clock.Now()
	if since, failing := condition.FailingSince(run.Status.Conditions, ConditionKeyMinted); failing && now.Sub(since) >= mintDeadline {
		return r.fail(ctx, run, v1alpha1.ReasonGatewayError, fmt.Sprintf("key mint failing since %s: %v", since.UTC().Format(time.RFC3339), err))
	}
	if perr := patch.Status(ctx, r.client, run, func() {
		r.setCondition(run, ConditionKeyMinted, metav1.ConditionFalse, reasonMintFailed, err.Error())
	}); perr != nil {
		return perr
	}
	return fmt.Errorf("mint key %s: %w", keyAlias(run), err)
}
