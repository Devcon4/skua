package profile

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
)

func (r *reconciler) readiness(ctx context.Context, p *v1alpha1.AgentProfile, poolName string, notStarting bool) (metav1.ConditionStatus, string, string) {
	var unknown, unpriced []string
	for _, model := range p.Spec.Models {
		info, err := r.gateway.ModelInfo(ctx, model)
		if err != nil {
			return metav1.ConditionFalse, v1alpha1.ReasonGatewayUnavailable, fmt.Sprintf("gateway model info for %q: %v", model, err)
		}
		switch {
		case !info.Known:
			unknown = append(unknown, model)
		case !info.Priced:
			unpriced = append(unpriced, model)
		}
	}
	switch {
	case len(unknown) > 0:
		return metav1.ConditionFalse, v1alpha1.ReasonModelUnknown, "gateway has no model group named " + strings.Join(unknown, ", ")
	case len(unpriced) > 0:
		return metav1.ConditionFalse, v1alpha1.ReasonModelUnpriced, "gateway has no input price for " + strings.Join(unpriced, ", ")
	case notStarting:
		return metav1.ConditionFalse, v1alpha1.ReasonPoolNotStarting, fmt.Sprintf("SandboxWarmPool %s has had no ready replicas for %s since its template last changed", poolName, r.timing.PoolStartWindow)
	}
	return metav1.ConditionTrue, v1alpha1.ReasonReady, "models are served and priced by the gateway"
}

func (r *reconciler) poolReadyReplicas(ctx context.Context, key client.ObjectKey) (int32, error) {
	var pool extv1beta1.SandboxWarmPool
	if err := r.client.Get(ctx, key, &pool); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("get SandboxWarmPool %s: %w", key, err)
	}
	return pool.Status.ReadyReplicas, nil
}

func (r *reconciler) observePool(p *v1alpha1.AgentProfile, templateHash [sha256.Size]byte, replicas, ready int32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	s, ok := r.pools[p.Name]
	if !ok || s.uid != p.UID || s.templateHash != templateHash || replicas == 0 {
		s = poolState{uid: p.UID, templateHash: templateHash, windowStart: now}
	}
	if ready > 0 {
		s.sawReady = true
	}
	r.pools[p.Name] = s
	return replicas > 0 && !s.sawReady && now.Sub(s.windowStart) >= r.timing.PoolStartWindow
}

func (r *reconciler) forgetPool(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pools, name)
}
