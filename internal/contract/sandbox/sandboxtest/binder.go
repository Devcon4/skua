// Package sandboxtest is a test stand-in for the agent-sandbox v1.0.9 claim and warm pool controllers.
// It binds SandboxClaims to pods in an envtest API server, which has no kubelet or garbage collector.
package sandboxtest

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// Options configures a Binder.
type Options struct {
	// ColdDelay is how long, on Clock, a claim with no ready pool pod waits before a pod is created
	// for it, counted from the Binder's first reconcile of the claim.
	ColdDelay time.Duration
	// Clock measures ColdDelay and stamps pod and condition times; nil means the real clock.
	Clock clock.PassiveClock
}

// Binder binds SandboxClaims the way the agent-sandbox controllers do and offers helpers that
// simulate pod failures. Helpers fail the test passed to Start on any API error.
type Binder struct {
	t         *testing.T
	client    client.Client
	reader    client.Reader
	clock     clock.PassiveClock
	coldDelay time.Duration

	mu        sync.Mutex
	firstSeen map[types.NamespacedName]coldWait
}

// Start registers the Binder as a SandboxClaim controller on mgr; the caller starts mgr.
// A claim adopts the oldest ready pod created by SetPoolReady for its warm pool, keeping that pod's
// creation time, and its claim gets the agents.x-k8s.io/sandbox-name annotation. With no such pod,
// a pod named after the claim is created from the pool's SandboxTemplate once ColdDelay has passed,
// and the claim gets no sandbox-name annotation. A bound pod carries the agents.x-k8s.io/claim-uid
// label and agents.x-k8s.io/claim-name annotation and is Running; the claim then has Ready=True and
// status.sandbox.name set, and is never rebound. Deleting a claim deletes its pod.
func Start(t *testing.T, mgr ctrl.Manager, opts Options) *Binder {
	t.Helper()
	b := &Binder{
		t:         t,
		client:    mgr.GetClient(),
		reader:    mgr.GetAPIReader(),
		clock:     opts.Clock,
		coldDelay: opts.ColdDelay,
		firstSeen: map[types.NamespacedName]coldWait{},
	}
	if b.clock == nil {
		b.clock = clock.RealClock{}
	}
	err := ctrl.NewControllerManagedBy(mgr).
		Named("sandboxtest").
		For(&extv1beta1.SandboxClaim{}).
		WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
		Complete(b)
	if err != nil {
		t.Fatalf("sandboxtest: register controller: %v", err)
	}
	return b
}

// Reconcile binds one SandboxClaim or deletes the pods of a deleted one.
func (b *Binder) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var claim extv1beta1.SandboxClaim
	if err := b.reader.Get(ctx, req.NamespacedName, &claim); err != nil {
		if apierrors.IsNotFound(err) {
			b.forget(req.NamespacedName)
			return ctrl.Result{}, b.deleteClaimPods(ctx, req.Namespace, req.Name, "")
		}
		return ctrl.Result{}, err
	}
	if !claim.DeletionTimestamp.IsZero() {
		b.forget(req.NamespacedName)
		return ctrl.Result{}, b.deleteClaimPods(ctx, req.Namespace, req.Name, "")
	}
	if err := b.deleteClaimPods(ctx, req.Namespace, req.Name, claim.UID); err != nil {
		return ctrl.Result{}, err
	}
	if claim.Status.SandboxStatus.Name != "" {
		return ctrl.Result{}, nil
	}

	pod, err := b.boundPod(ctx, &claim)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod == nil {
		if pod, err = b.adopt(ctx, &claim); err != nil {
			return ctrl.Result{}, err
		}
	}
	if pod == nil {
		if b.clock.Since(b.waitingSince(req.NamespacedName, claim.UID)) < b.coldDelay {
			return ctrl.Result{RequeueAfter: coldPollInterval}, nil
		}
		if pod, err = b.createCold(ctx, &claim); err != nil {
			return ctrl.Result{}, err
		}
	}
	if pod.Status.Phase == "" || pod.Status.Phase == corev1.PodPending {
		if err := b.markRunning(ctx, pod); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := b.markClaimReady(ctx, &claim, pod.Name, pod.Name != claim.Name); err != nil {
		return ctrl.Result{}, err
	}
	b.forget(req.NamespacedName)
	return ctrl.Result{}, nil
}

func (b *Binder) boundPod(ctx context.Context, claim *extv1beta1.SandboxClaim) (*corev1.Pod, error) {
	var pods corev1.PodList
	if err := b.reader.List(ctx, &pods, client.InNamespace(claim.Namespace),
		client.MatchingLabels{extv1beta1.SandboxIDLabel: string(claim.UID)}); err != nil {
		return nil, err
	}
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp.IsZero() {
			return &pods.Items[i], nil
		}
	}
	return nil, nil
}

func (b *Binder) adopt(ctx context.Context, claim *extv1beta1.SandboxClaim) (*corev1.Pod, error) {
	pool := client.ObjectKey{Namespace: claim.Namespace, Name: claim.Spec.WarmPoolRef.Name}
	pods, err := b.poolPods(ctx, pool)
	if err != nil {
		return nil, err
	}
	var ready []corev1.Pod
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning {
			ready = append(ready, p)
		}
	}
	if len(ready) == 0 {
		return nil, nil
	}
	slices.SortFunc(ready, func(x, y corev1.Pod) int {
		if c := x.CreationTimestamp.Compare(y.CreationTimestamp.Time); c != 0 {
			return c
		}
		return cmp.Compare(x.Name, y.Name)
	})
	pod := &ready[0]
	delete(pod.Labels, sandboxv1beta1.SandboxWarmPoolLabel)
	applyClaimMetadata(&pod.ObjectMeta, claim)
	if err := b.client.Update(ctx, pod); err != nil {
		return nil, fmt.Errorf("adopt pool pod %s: %w", pod.Name, err)
	}
	if err := b.syncPoolStatus(ctx, pool); err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	return pod, nil
}

func applyClaimMetadata(m *metav1.ObjectMeta, claim *extv1beta1.SandboxClaim) {
	if m.Labels == nil {
		m.Labels = map[string]string{}
	}
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	maps.Copy(m.Labels, claim.Spec.AdditionalPodMetadata.Labels)
	maps.Copy(m.Annotations, claim.Spec.AdditionalPodMetadata.Annotations)
	m.Labels[extv1beta1.SandboxIDLabel] = string(claim.UID)
	m.Annotations[extv1beta1.SandboxClaimNameAnnotation] = claim.Name
}

func (b *Binder) markClaimReady(ctx context.Context, claim *extv1beta1.SandboxClaim, podName string, adopted bool) error {
	key := client.ObjectKeyFromObject(claim)
	if adopted {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var c extv1beta1.SandboxClaim
			if err := b.reader.Get(ctx, key, &c); err != nil {
				return err
			}
			if c.Annotations[extv1beta1.AssignedSandboxNameAnnotation] == podName {
				return nil
			}
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[extv1beta1.AssignedSandboxNameAnnotation] = podName
			return b.client.Update(ctx, &c)
		})
		if err != nil {
			return fmt.Errorf("annotate claim %s: %w", claim.Name, err)
		}
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var c extv1beta1.SandboxClaim
		if err := b.reader.Get(ctx, key, &c); err != nil {
			return err
		}
		c.Status.SandboxStatus.Name = podName
		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type:               string(sandboxv1beta1.SandboxConditionReady),
			Status:             metav1.ConditionTrue,
			Reason:             sandboxv1beta1.SandboxReasonDependenciesReady,
			Message:            "Pod is Ready",
			ObservedGeneration: c.Generation,
			LastTransitionTime: b.now(),
		})
		return b.client.Status().Update(ctx, &c)
	})
}

func (b *Binder) deleteClaimPods(ctx context.Context, namespace, claimName string, keep types.UID) error {
	var pods corev1.PodList
	if err := b.reader.List(ctx, &pods, client.InNamespace(namespace)); err != nil {
		return err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Annotations[extv1beta1.SandboxClaimNameAnnotation] != claimName {
			continue
		}
		if keep != "" && p.Labels[extv1beta1.SandboxIDLabel] == string(keep) {
			continue
		}
		if err := b.client.Delete(ctx, p, client.GracePeriodSeconds(0)); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete pod %s of claim %s: %w", p.Name, claimName, err)
		}
	}
	return nil
}

func (b *Binder) markRunning(ctx context.Context, pod *corev1.Pod) error {
	now := b.now()
	pod.Status.Phase = corev1.PodRunning
	pod.Status.StartTime = &now
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: corev1.PodInitialized, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: corev1.ContainersReady, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: now},
	}
	pod.Status.InitContainerStatuses = nil
	for _, c := range pod.Spec.InitContainers {
		pod.Status.InitContainerStatuses = append(pod.Status.InitContainerStatuses, corev1.ContainerStatus{
			Name:  c.Name,
			Image: c.Image,
			Ready: true,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Completed", StartedAt: now, FinishedAt: now,
			}},
		})
	}
	pod.Status.ContainerStatuses = nil
	for _, c := range pod.Spec.Containers {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name:    c.Name,
			Image:   c.Image,
			Ready:   true,
			Started: ptr.To(true),
			State:   corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}},
		})
	}
	if err := b.client.Status().Update(ctx, pod); err != nil {
		return fmt.Errorf("mark pod %s running: %w", pod.Name, err)
	}
	return nil
}

func (b *Binder) now() metav1.Time {
	return metav1.NewTime(b.clock.Now().Truncate(time.Second))
}
