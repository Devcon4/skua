package profile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FieldManager owns the SandboxTemplate and SandboxWarmPool fields the reconciler server-side applies.
const FieldManager = "skua-operator"

func apply(ctx context.Context, c client.Client, obj client.Object) error {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return fmt.Errorf("convert to unstructured: %w", err)
	}
	delete(u, "status")
	unstructured.RemoveNestedField(u, "metadata", "creationTimestamp")
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: u}),
		client.FieldOwner(FieldManager), client.ForceOwnership)
}

func templateHash(tmpl *extv1beta1.SandboxTemplate) ([sha256.Size]byte, error) {
	data, err := json.Marshal(tmpl.Spec)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("hash SandboxTemplate %s/%s: %w", tmpl.Namespace, tmpl.Name, err)
	}
	return sha256.Sum256(data), nil
}
