package run

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/operator/patch"
)

// Claim renders the SandboxClaim of run in cfg.SandboxNamespace. It has no owner reference because it lives
// outside the run's namespace; its labels name the run.
func Claim(run *v1alpha1.AgentRun, cfg *config.Config) *extv1beta1.SandboxClaim {
	return &extv1beta1.SandboxClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: extv1beta1.GroupVersion.String(), Kind: "SandboxClaim"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      run.Name,
			Namespace: cfg.SandboxNamespace,
			Labels: map[string]string{
				sandbox.LabelRunName: run.Name,
				sandbox.LabelRunUID:  string(run.UID),
				sandbox.LabelProfile: run.Spec.Profile,
			},
		},
		Spec: extv1beta1.SandboxClaimSpec{
			WarmPoolRef: extv1beta1.SandboxWarmPoolRef{Name: sandbox.Name(run.Spec.Profile)},
		},
	}
}

func (r *reconciler) claimToRun(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[sandbox.LabelRunName]
	if name == "" || obj.GetNamespace() != r.deps.Config.SandboxNamespace {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: r.deps.Config.RunNamespace, Name: name}}}
}

func (r *reconciler) ensureClaim(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	want := Claim(run, r.deps.Config)
	var claim extv1beta1.SandboxClaim
	err := r.client.Get(ctx, client.ObjectKeyFromObject(want), &claim)
	if apierrors.IsNotFound(err) {
		if err := r.client.Create(ctx, want); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create claim %s: %w", want.Name, err)
		}
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get claim %s: %w", want.Name, err)
	}
	if uid := claim.Labels[sandbox.LabelRunUID]; uid != string(run.UID) {
		return ctrl.Result{}, fmt.Errorf("claim %s belongs to run uid %q, not %s", claim.Name, uid, run.UID)
	}
	if !meta.IsStatusConditionTrue(claim.Status.Conditions, string(sandboxv1beta1.SandboxConditionReady)) {
		return ctrl.Result{}, nil
	}

	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, client.InNamespace(claim.Namespace),
		client.MatchingLabels{extv1beta1.SandboxIDLabel: string(claim.UID)}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list pods of claim %s: %w", claim.Name, err)
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp.IsZero() {
			pod = &pods.Items[i]
			break
		}
	}
	if pod == nil {
		return ctrl.Result{RequeueAfter: r.deps.Timing.PodPoll}, nil
	}
	now := metav1.NewTime(r.deps.Clock.Now())
	return ctrl.Result{}, patch.Status(ctx, r.client, run, func() {
		run.Status.Claim = claim.Name
		run.Status.PodName = pod.Name
		run.Status.PodUID = pod.UID
		run.Status.StartedAt = &now
		run.Status.Phase = v1alpha1.PhaseRunning
		run.Status.Message = ""
	})
}
