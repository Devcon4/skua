package sandboxtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (b *Binder) poolPods(ctx context.Context, pool client.ObjectKey) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := b.reader.List(ctx, &pods, client.InNamespace(pool.Namespace),
		client.MatchingLabels{sandboxv1beta1.SandboxWarmPoolLabel: poolLabelValue(pool.Name)}); err != nil {
		return nil, err
	}
	var out []corev1.Pod
	for _, p := range pods.Items {
		if p.DeletionTimestamp.IsZero() && p.Labels[extv1beta1.SandboxIDLabel] == "" {
			out = append(out, p)
		}
	}
	return out, nil
}

func (b *Binder) podFromPool(ctx context.Context, pool client.ObjectKey) (*corev1.Pod, error) {
	var wp extv1beta1.SandboxWarmPool
	if err := b.reader.Get(ctx, pool, &wp); err != nil {
		return nil, fmt.Errorf("get warm pool %s: %w", pool, err)
	}
	var tmpl extv1beta1.SandboxTemplate
	tmplKey := client.ObjectKey{Namespace: pool.Namespace, Name: wp.Spec.TemplateRef.Name}
	if err := b.reader.Get(ctx, tmplKey, &tmpl); err != nil {
		return nil, fmt.Errorf("get sandbox template %s: %w", tmplKey, err)
	}
	pt := tmpl.Spec.PodTemplate
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   pool.Namespace,
			Labels:      maps.Clone(pt.ObjectMeta.Labels),
			Annotations: maps.Clone(pt.ObjectMeta.Annotations),
		},
		Spec: *pt.Spec.DeepCopy(),
	}, nil
}

func (b *Binder) syncPoolStatus(ctx context.Context, pool client.ObjectKey) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var wp extv1beta1.SandboxWarmPool
		if err := b.reader.Get(ctx, pool, &wp); err != nil {
			return err
		}
		pods, err := b.poolPods(ctx, pool)
		if err != nil {
			return err
		}
		var ready int32
		for _, p := range pods {
			if p.Status.Phase == corev1.PodRunning {
				ready++
			}
		}
		wp.Status.Replicas = int32(len(pods))
		wp.Status.ReadyReplicas = ready
		wp.Status.Selector = sandboxv1beta1.SandboxWarmPoolLabel + "=" + poolLabelValue(pool.Name)
		wp.Status.ObservedGeneration = wp.Generation
		return b.client.Status().Update(ctx, &wp)
	})
}

// SetPoolReady makes the warm pool hold exactly n unclaimed Running pods built from its
// SandboxTemplate, creating or deleting pool pods, and sets the pool's status replicas to match.
// The pool and its template must exist.
func (b *Binder) SetPoolReady(pool client.ObjectKey, n int) {
	b.t.Helper()
	ctx := b.t.Context()
	pods, err := b.poolPods(ctx, pool)
	if err != nil {
		b.t.Fatalf("sandboxtest: list pool pods of %s: %v", pool, err)
	}
	for i := len(pods); i < n; i++ {
		pod, err := b.podFromPool(ctx, pool)
		if err != nil {
			b.t.Fatalf("sandboxtest: %v", err)
		}
		pod.GenerateName = pool.Name + "-"
		if pod.Labels == nil {
			pod.Labels = map[string]string{}
		}
		pod.Labels[sandboxv1beta1.SandboxWarmPoolLabel] = poolLabelValue(pool.Name)
		if err := b.client.Create(ctx, pod); err != nil {
			b.t.Fatalf("sandboxtest: create pool pod for %s: %v", pool, err)
		}
		if err := b.markRunning(ctx, pod); err != nil {
			b.t.Fatalf("sandboxtest: %v", err)
		}
	}
	if len(pods) > n {
		slices.SortFunc(pods, func(x, y corev1.Pod) int {
			return y.CreationTimestamp.Compare(x.CreationTimestamp.Time)
		})
		for i := range pods[:len(pods)-n] {
			if err := b.client.Delete(ctx, &pods[i], client.GracePeriodSeconds(0)); client.IgnoreNotFound(err) != nil {
				b.t.Fatalf("sandboxtest: delete pool pod %s: %v", pods[i].Name, err)
			}
		}
	}
	if err := b.syncPoolStatus(ctx, pool); err != nil {
		b.t.Fatalf("sandboxtest: update status of pool %s: %v", pool, err)
	}
}

func poolLabelValue(pool string) string {
	sum := sha256.Sum256([]byte(pool))
	return hex.EncodeToString(sum[:4])
}
