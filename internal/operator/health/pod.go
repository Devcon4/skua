package health

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
)

func (r *reconciler) podToRun(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	run, err := sandbox.RunForPod(ctx, r.client, r.client, r.cfg.SandboxNamespace, r.cfg.RunNamespace, pod)
	if err != nil {
		log.FromContext(ctx).Error(err, "map pod to run", "pod", client.ObjectKeyFromObject(pod))
		return nil
	}
	if run == nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(run)}}
}

// podFailure reads the run's pod uncached, so a pod the cache has not seen yet is never reported lost.
func (r *reconciler) podFailure(ctx context.Context, run *v1alpha1.AgentRun, limits v1alpha1.Limits) (reason, message string, err error) {
	name := run.Status.PodName
	var pod corev1.Pod
	err = r.apiReader.Get(ctx, client.ObjectKey{Namespace: r.cfg.SandboxNamespace, Name: name}, &pod)
	if apierrors.IsNotFound(err) {
		return v1alpha1.ReasonPodLost, fmt.Sprintf("pod %s is gone", name), nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get pod %s: %w", name, err)
	}
	if pod.UID != run.Status.PodUID {
		return v1alpha1.ReasonPodLost, fmt.Sprintf("pod %s was replaced", name), nil
	}

	owner, err := sandbox.RunForPod(ctx, r.apiReader, r.apiReader, r.cfg.SandboxNamespace, r.cfg.RunNamespace, &pod)
	if err != nil {
		return "", "", fmt.Errorf("look up run of pod %s: %w", name, err)
	}
	if owner == nil || owner.UID != run.UID {
		return "", "", nil
	}

	if pod.Status.Phase == corev1.PodFailed {
		return v1alpha1.ReasonPodLost, podFailedMessage(&pod), nil
	}
	if limits.MaxRestarts == nil {
		return "", "", nil
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == sandbox.AgentContainerName && cs.RestartCount > *limits.MaxRestarts {
			return v1alpha1.ReasonCrashLoop, fmt.Sprintf("agent container restarted %d times, maxRestarts is %d",
				cs.RestartCount, *limits.MaxRestarts), nil
		}
	}
	return "", "", nil
}

func podFailedMessage(pod *corev1.Pod) string {
	msg := "pod " + pod.Name + " failed"
	if pod.Status.Reason != "" {
		msg += ": " + pod.Status.Reason
	}
	if pod.Status.Message != "" {
		msg += ": " + pod.Status.Message
	}
	return msg
}
