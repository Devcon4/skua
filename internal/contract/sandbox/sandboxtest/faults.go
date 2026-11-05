package sandboxtest

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Evict fails the pod as a kubelet eviction does: phase Failed, reason Evicted, message msg,
// every container terminated and the pod not Ready.
func (b *Binder) Evict(pod client.ObjectKey, msg string) {
	b.t.Helper()
	b.updatePodStatus(pod, func(p *corev1.Pod) {
		now := b.now()
		p.Status.Phase = corev1.PodFailed
		p.Status.Reason = "Evicted"
		p.Status.Message = msg
		for i := range p.Status.Conditions {
			if c := &p.Status.Conditions[i]; c.Type == corev1.PodReady || c.Type == corev1.ContainersReady {
				c.Status = corev1.ConditionFalse
				c.LastTransitionTime = now
			}
		}
		for i := range p.Status.ContainerStatuses {
			cs := &p.Status.ContainerStatuses[i]
			started := now
			if cs.State.Running != nil {
				started = cs.State.Running.StartedAt
			}
			cs.Ready = false
			cs.Started = ptr.To(false)
			cs.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137, Reason: "Evicted", Message: msg, StartedAt: started, FinishedAt: now,
			}}
		}
	})
}

// RestartContainer sets the restart count of the pod's first container, the agent container
// in the skua pod layout, to n and records an exit code 1 as its last termination.
func (b *Binder) RestartContainer(pod client.ObjectKey, n int32) {
	b.t.Helper()
	b.updatePodStatus(pod, func(p *corev1.Pod) {
		if len(p.Status.ContainerStatuses) == 0 {
			b.t.Fatalf("sandboxtest: pod %s has no container statuses", pod)
		}
		now := b.now()
		cs := &p.Status.ContainerStatuses[0]
		cs.RestartCount = n
		cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 1, Reason: "Error", StartedAt: now, FinishedAt: now,
		}}
	})
}

// DeletePod deletes the pod immediately and recreates it as the agent-sandbox Sandbox controller
// does: same name, labels and annotations, a new UID, and Running. The claim keeps its binding.
func (b *Binder) DeletePod(pod client.ObjectKey) {
	b.t.Helper()
	ctx := b.t.Context()
	var old corev1.Pod
	if err := b.reader.Get(ctx, pod, &old); err != nil {
		b.t.Fatalf("sandboxtest: get pod %s: %v", pod, err)
	}
	b.LosePod(pod)
	fresh := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   old.Namespace,
			Name:        old.Name,
			Labels:      old.Labels,
			Annotations: old.Annotations,
		},
		Spec: old.Spec,
	}
	if err := b.client.Create(ctx, fresh); err != nil {
		b.t.Fatalf("sandboxtest: recreate pod %s: %v", pod, err)
	}
	if err := b.markRunning(ctx, fresh); err != nil {
		b.t.Fatalf("sandboxtest: %v", err)
	}
}

// LosePod deletes the pod immediately and never recreates it, as with a lost node. Its claim is
// not rebound.
func (b *Binder) LosePod(pod client.ObjectKey) {
	b.t.Helper()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name}}
	if err := b.client.Delete(b.t.Context(), p, client.GracePeriodSeconds(0)); err != nil {
		b.t.Fatalf("sandboxtest: delete pod %s: %v", pod, err)
	}
}

func (b *Binder) updatePodStatus(key client.ObjectKey, mutate func(*corev1.Pod)) {
	b.t.Helper()
	ctx := b.t.Context()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var p corev1.Pod
		if err := b.reader.Get(ctx, key, &p); err != nil {
			return err
		}
		mutate(&p)
		return b.client.Status().Update(ctx, &p)
	})
	if err != nil {
		b.t.Fatalf("sandboxtest: update status of pod %s: %v", key, err)
	}
}
