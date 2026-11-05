package sandboxtest

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const coldPollInterval = 50 * time.Millisecond

type coldWait struct {
	uid   types.UID
	since time.Time
}

// Waiting reports whether the claim has been reconciled and is waiting for ColdDelay to pass
// before its pod is created.
func (b *Binder) Waiting(claim client.ObjectKey) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.firstSeen[claim]
	return ok
}

func (b *Binder) waitingSince(key types.NamespacedName, uid types.UID) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	w, ok := b.firstSeen[key]
	if !ok || w.uid != uid {
		w = coldWait{uid: uid, since: b.clock.Now()}
		b.firstSeen[key] = w
	}
	return w.since
}

func (b *Binder) forget(key types.NamespacedName) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.firstSeen, key)
}

func (b *Binder) createCold(ctx context.Context, claim *extv1beta1.SandboxClaim) (*corev1.Pod, error) {
	pool := client.ObjectKey{Namespace: claim.Namespace, Name: claim.Spec.WarmPoolRef.Name}
	pod, err := b.podFromPool(ctx, pool)
	if err != nil {
		return nil, err
	}
	pod.Name = claim.Name
	applyClaimMetadata(&pod.ObjectMeta, claim)
	if err := b.client.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("create pod for claim %s: %w", claim.Name, err)
	}
	return pod, nil
}
