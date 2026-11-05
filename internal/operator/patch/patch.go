// Package patch writes merge patches with optimistic locking for the operator's reconcilers.
package patch

import (
	"bytes"
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var emptyMergePatch = []byte("{}")

// Object applies mutate to obj and sends the difference as a merge patch guarded by obj's resourceVersion.
// An empty difference sends nothing.
func Object(ctx context.Context, c client.Client, obj client.Object, mutate func()) error {
	return patch(ctx, c, obj, false, mutate)
}

// Status is Object for the status subresource.
func Status(ctx context.Context, c client.Client, obj client.Object, mutate func()) error {
	return patch(ctx, c, obj, true, mutate)
}

func patch(ctx context.Context, c client.Client, obj client.Object, status bool, mutate func()) error {
	base, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("deep copy of %T is not a client.Object", obj)
	}
	mutate()
	data, err := client.MergeFrom(base).Data(obj)
	if err != nil {
		return fmt.Errorf("compute patch: %w", err)
	}
	if len(data) == 0 || bytes.Equal(data, emptyMergePatch) {
		return nil
	}
	p := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if status {
		return c.Status().Patch(ctx, obj, p)
	}
	return c.Patch(ctx, obj, p)
}
