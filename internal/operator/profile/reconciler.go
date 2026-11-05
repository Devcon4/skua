package profile

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/integration/litellm"
	"github.com/Devcon4/skua/internal/operator/condition"
	"github.com/Devcon4/skua/internal/operator/patch"
)

type reconciler struct {
	client  client.Client
	cfg     *config.Config
	gateway litellm.Provider
	clock   clock.PassiveClock
	compat  Compat
	timing  Timing

	mu    sync.Mutex
	pools map[string]poolState
}

// In memory: an operator restart opens a new window, which delays PoolNotStarting but never raises it early.
type poolState struct {
	uid          types.UID
	templateHash [sha256.Size]byte
	windowStart  time.Time
	sawReady     bool
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p v1alpha1.AgentProfile
	if err := r.client.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			r.forgetPool(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get AgentProfile %s: %w", req.Name, err)
	}
	if !p.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if r.compat != nil {
		if msg, unsupported := r.compat.Unsupported(); unsupported {
			if err := r.writeReady(ctx, &p, metav1.ConditionFalse, v1alpha1.ReasonAgentSandboxUnsupported, msg); err != nil {
				return ctrl.Result{}, fmt.Errorf("patch status of AgentProfile %s: %w", p.Name, err)
			}
			return ctrl.Result{RequeueAfter: r.timing.Recheck}, nil
		}
	}

	tmpl := Template(&p, r.cfg)
	if err := apply(ctx, r.client, tmpl); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply SandboxTemplate %s/%s: %w", tmpl.Namespace, tmpl.Name, err)
	}
	pool := WarmPool(&p, r.cfg)
	if err := apply(ctx, r.client, pool); err != nil {
		return ctrl.Result{}, fmt.Errorf("apply SandboxWarmPool %s/%s: %w", pool.Namespace, pool.Name, err)
	}

	hash, err := templateHash(tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	ready, err := r.poolReadyReplicas(ctx, client.ObjectKeyFromObject(pool))
	if err != nil {
		return ctrl.Result{}, err
	}
	notStarting := r.observePool(&p, hash, ptr.Deref(pool.Spec.Replicas, 0), ready)

	status, reason, message := r.readiness(ctx, &p, pool.Name, notStarting)
	if err := r.writeReady(ctx, &p, status, reason, message); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status of AgentProfile %s: %w", p.Name, err)
	}
	return ctrl.Result{RequeueAfter: r.timing.Recheck}, nil
}

func (r *reconciler) writeReady(ctx context.Context, p *v1alpha1.AgentProfile, status metav1.ConditionStatus, reason, message string) error {
	return patch.Status(ctx, r.client, p, func() {
		condition.Set(&p.Status.Conditions, p.Generation, v1alpha1.ConditionReady, status, reason, message, r.clock.Now())
		p.Status.ObservedGeneration = p.Generation
	})
}
