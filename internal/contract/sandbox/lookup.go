package sandbox

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
)

// RunForPod returns the AgentRun in runNamespace that pod serves. The pod must sit in sandboxNamespace,
// name its SandboxClaim, carry that claim's UID, and be the pod the run's status records. claims reads the
// claim and runs reads the run. It returns nil, nil when the pod maps to no run, and only read errors other
// than NotFound.
func RunForPod(ctx context.Context, claims, runs client.Reader, sandboxNamespace, runNamespace string, pod *corev1.Pod) (*v1alpha1.AgentRun, error) {
	claimName := pod.Annotations[extv1beta1.SandboxClaimNameAnnotation]
	if pod.Namespace != sandboxNamespace || claimName == "" {
		return nil, nil
	}
	var claim extv1beta1.SandboxClaim
	if err := claims.Get(ctx, client.ObjectKey{Namespace: sandboxNamespace, Name: claimName}, &claim); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	runName := claim.Labels[LabelRunName]
	if string(claim.UID) != pod.Labels[extv1beta1.SandboxIDLabel] || runName == "" {
		return nil, nil
	}
	var run v1alpha1.AgentRun
	if err := runs.Get(ctx, client.ObjectKey{Namespace: runNamespace, Name: runName}, &run); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if string(run.UID) != claim.Labels[LabelRunUID] || run.Status.PodUID != pod.UID {
		return nil, nil
	}
	return &run, nil
}
