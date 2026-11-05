package operator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGatewayKeyReadsConfiguredNamespace(t *testing.T) {
	cfg := operatorTestConfig(t)
	ref := cfg.Gateway.OperatorKeySecret
	secret := func(namespace, value string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: ref.Name},
			Data:       map[string][]byte{ref.Key: []byte(value)},
		}
	}
	c := fake.NewClientBuilder().WithObjects(secret(cfg.RunNamespace, "sk-decoy"), secret(ref.Namespace, "sk-operator\n")).Build()

	got, err := gatewayKey(t.Context(), c, cfg)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if ref.Namespace != "skua-system" || got != "sk-operator" {
		t.Errorf("key %q from namespace %q, want sk-operator from skua-system", got, ref.Namespace)
	}
}
