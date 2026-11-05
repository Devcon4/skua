package operator

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/internal/contract/config"
)

func gatewayKey(ctx context.Context, reader client.Reader, cfg *config.Config) (string, error) {
	ref := cfg.Gateway.OperatorKeySecret
	var secret corev1.Secret
	if err := reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &secret); err != nil {
		return "", fmt.Errorf("read LiteLLM operator key secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	key := strings.TrimSpace(string(secret.Data[ref.Key]))
	if key == "" {
		return "", fmt.Errorf("LiteLLM operator key secret %s/%s has no key %q", ref.Namespace, ref.Name, ref.Key)
	}
	return key, nil
}
