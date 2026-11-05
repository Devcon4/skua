package run

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/operator/patch"
)

func (r *reconciler) admit(ctx context.Context, run *v1alpha1.AgentRun) error {
	var profile v1alpha1.AgentProfile
	err := r.deps.APIReader.Get(ctx, client.ObjectKey{Name: run.Spec.Profile}, &profile)
	if apierrors.IsNotFound(err) {
		return r.fail(ctx, run, v1alpha1.ReasonProfileNotFound, fmt.Sprintf("profile %q does not exist", run.Spec.Profile))
	}
	if err != nil {
		return fmt.Errorf("get profile %s: %w", run.Spec.Profile, err)
	}
	ready := meta.FindStatusCondition(profile.Status.Conditions, v1alpha1.ConditionReady)
	if ready != nil && ready.ObservedGeneration != profile.Generation {
		return fmt.Errorf("profile %s Ready condition is for generation %d, not %d", profile.Name, ready.ObservedGeneration, profile.Generation)
	}
	if ready == nil || ready.Status != metav1.ConditionTrue {
		msg := fmt.Sprintf("profile %q is not Ready", run.Spec.Profile)
		if ready != nil && ready.Reason != "" {
			msg += ": " + ready.Reason
		}
		return r.fail(ctx, run, v1alpha1.ReasonProfileNotReady, msg)
	}
	return patch.Status(ctx, r.client, run, func() {
		run.Status.ProfileSnapshot = profile.Spec.DeepCopy()
		run.Status.Phase = v1alpha1.PhasePending
	})
}
