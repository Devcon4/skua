package sandboxtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Devcon4/skua/internal/contract/sandbox/sandboxtest"
	"github.com/Devcon4/skua/internal/testenv"
)

const testNamespace = "skua-sandboxes"

type env struct {
	c      client.Client
	binder *sandboxtest.Binder
	clock  *clocktesting.FakeClock
}

func startEnv(t *testing.T, coldDelay time.Duration) *env {
	t.Helper()
	restCfg, c := testenv.Start(t)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	if err := c.Create(t.Context(), ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:     c.Scheme(),
		Logger:     testr.New(t),
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	clk := clocktesting.NewFakeClock(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	binder := sandboxtest.Start(t, mgr, sandboxtest.Options{ColdDelay: coldDelay, Clock: clk})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("manager: %v", err)
		}
	})
	return &env{c: c, binder: binder, clock: clk}
}

func (e *env) createPool(t *testing.T, name string) client.ObjectKey {
	t.Helper()
	tmpl := &extv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: name},
		Spec: extv1beta1.SandboxTemplateSpec{SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
			PodTemplate: sandboxv1beta1.PodTemplate{
				ObjectMeta: sandboxv1beta1.PodMetadata{Labels: map[string]string{"skua.dev/profile": name}},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{Name: "install-runner", Image: "skua:dev"}},
					Containers:     []corev1.Container{{Name: "agent", Image: "generic:dev"}},
				},
			},
		}},
	}
	if err := e.c.Create(t.Context(), tmpl); err != nil {
		t.Fatalf("create template: %v", err)
	}
	pool := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: name},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    ptr.To[int32](1),
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: name},
		},
	}
	if err := e.c.Create(t.Context(), pool); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	return client.ObjectKeyFromObject(pool)
}

func (e *env) createClaim(t *testing.T, name, pool string) *extv1beta1.SandboxClaim {
	t.Helper()
	claim := &extv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: name},
		Spec:       extv1beta1.SandboxClaimSpec{WarmPoolRef: extv1beta1.SandboxWarmPoolRef{Name: pool}},
	}
	if err := e.c.Create(t.Context(), claim); err != nil {
		t.Fatalf("create claim: %v", err)
	}
	return claim
}

func (e *env) waitReady(t *testing.T, name string) *extv1beta1.SandboxClaim {
	t.Helper()
	var claim extv1beta1.SandboxClaim
	eventually(t, "claim "+name+" Ready", func() bool {
		if err := e.c.Get(t.Context(), client.ObjectKey{Namespace: testNamespace, Name: name}, &claim); err != nil {
			t.Fatalf("get claim: %v", err)
		}
		return meta.IsStatusConditionTrue(claim.Status.Conditions, string(sandboxv1beta1.SandboxConditionReady))
	})
	return &claim
}

func (e *env) pod(t *testing.T, name string) (*corev1.Pod, bool) {
	t.Helper()
	var pod corev1.Pod
	err := e.c.Get(t.Context(), client.ObjectKey{Namespace: testNamespace, Name: name}, &pod)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get pod %s: %v", name, err)
	}
	return &pod, true
}

func (e *env) poolStatus(t *testing.T, pool client.ObjectKey) extv1beta1.SandboxWarmPoolStatus {
	t.Helper()
	var wp extv1beta1.SandboxWarmPool
	if err := e.c.Get(t.Context(), pool, &wp); err != nil {
		t.Fatalf("get pool: %v", err)
	}
	return wp.Status
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertBound(t *testing.T, pod *corev1.Pod, claim *extv1beta1.SandboxClaim) {
	t.Helper()
	if got := pod.Labels[extv1beta1.SandboxIDLabel]; got != string(claim.UID) {
		t.Errorf("pod claim-uid label = %q, want %q", got, claim.UID)
	}
	if got := pod.Annotations[extv1beta1.SandboxClaimNameAnnotation]; got != claim.Name {
		t.Errorf("pod claim-name annotation = %q, want %q", got, claim.Name)
	}
	if pod.Status.Phase != corev1.PodRunning {
		t.Errorf("pod phase = %q, want Running", pod.Status.Phase)
	}
	if claim.Status.SandboxStatus.Name != pod.Name {
		t.Errorf("claim status.sandbox.name = %q, want %q", claim.Status.SandboxStatus.Name, pod.Name)
	}
}

func TestBinder(t *testing.T) {
	e := startEnv(t, 30*time.Second)

	t.Run("claim adopts a ready pool pod and keeps its creation time", func(t *testing.T) {
		pool := e.createPool(t, "skua-warm")
		e.binder.SetPoolReady(pool, 1)
		if st := e.poolStatus(t, pool); st.ReadyReplicas != 1 || st.Replicas != 1 {
			t.Fatalf("pool status after SetPoolReady = %+v, want 1 ready", st)
		}
		var pods corev1.PodList
		if err := e.c.List(t.Context(), &pods, client.InNamespace(testNamespace),
			client.MatchingLabels{"skua.dev/profile": "skua-warm"}); err != nil {
			t.Fatalf("list pool pods: %v", err)
		}
		if len(pods.Items) != 1 || pods.Items[0].Status.Phase != corev1.PodRunning {
			t.Fatalf("pool pods = %d, want one Running", len(pods.Items))
		}
		warm := pods.Items[0]
		e.createClaim(t, "run-warm", pool.Name)
		claim := e.waitReady(t, "run-warm")
		pod, ok := e.pod(t, warm.Name)
		if !ok {
			t.Fatalf("pool pod %s is gone", warm.Name)
		}
		if pod.UID != warm.UID || !pod.CreationTimestamp.Equal(&warm.CreationTimestamp) {
			t.Errorf("adopted pod uid %s created %s, want uid %s created %s", pod.UID, pod.CreationTimestamp, warm.UID, warm.CreationTimestamp)
		}
		assertBound(t, pod, claim)
		if got := claim.Annotations[extv1beta1.AssignedSandboxNameAnnotation]; got != warm.Name {
			t.Errorf("claim sandbox-name annotation = %q, want %q", got, warm.Name)
		}
		if _, ok := pod.Labels[sandboxv1beta1.SandboxWarmPoolLabel]; ok {
			t.Error("adopted pod still carries the warm pool label")
		}
		if st := e.poolStatus(t, pool); st.ReadyReplicas != 0 {
			t.Errorf("pool readyReplicas after adoption = %d, want 0", st.ReadyReplicas)
		}
	})

	t.Run("claim with no pool pod is bound after ColdDelay", func(t *testing.T) {
		pool := e.createPool(t, "skua-cold")
		claimKey := client.ObjectKeyFromObject(e.createClaim(t, "run-cold", pool.Name))
		eventually(t, "binder waiting on run-cold", func() bool { return e.binder.Waiting(claimKey) })
		time.Sleep(200 * time.Millisecond)
		if _, ok := e.pod(t, "run-cold"); ok {
			t.Fatal("pod created before ColdDelay passed on the clock")
		}
		e.clock.Step(30 * time.Second)
		claim := e.waitReady(t, "run-cold")
		eventually(t, "binder done waiting on run-cold", func() bool { return !e.binder.Waiting(claimKey) })
		pod, ok := e.pod(t, "run-cold")
		if !ok {
			t.Fatal("no pod for the cold claim")
		}
		assertBound(t, pod, claim)
		if _, ok := claim.Annotations[extv1beta1.AssignedSandboxNameAnnotation]; ok {
			t.Error("cold claim has a sandbox-name annotation")
		}
		if pod.Labels["skua.dev/profile"] != "skua-cold" || pod.Spec.Containers[0].Name != "agent" {
			t.Errorf("cold pod not built from the template: labels %v", pod.Labels)
		}
	})

	t.Run("Evict fails the pod with the message", func(t *testing.T) {
		pool := e.createPool(t, "skua-evict")
		e.binder.SetPoolReady(pool, 1)
		e.createClaim(t, "run-evict", pool.Name)
		claim := e.waitReady(t, "run-evict")
		key := client.ObjectKey{Namespace: testNamespace, Name: claim.Status.SandboxStatus.Name}
		e.binder.Evict(key, "The node was low on resource: memory.")
		pod, _ := e.pod(t, key.Name)
		if pod.Status.Phase != corev1.PodFailed || pod.Status.Reason != "Evicted" || pod.Status.Message != "The node was low on resource: memory." {
			t.Errorf("evicted pod status = %s/%s %q", pod.Status.Phase, pod.Status.Reason, pod.Status.Message)
		}
	})

	t.Run("RestartContainer sets restartCount", func(t *testing.T) {
		pool := e.createPool(t, "skua-restart")
		e.binder.SetPoolReady(pool, 1)
		e.createClaim(t, "run-restart", pool.Name)
		claim := e.waitReady(t, "run-restart")
		key := client.ObjectKey{Namespace: testNamespace, Name: claim.Status.SandboxStatus.Name}
		e.binder.RestartContainer(key, 2)
		pod, _ := e.pod(t, key.Name)
		if got := pod.Status.ContainerStatuses[0].RestartCount; got != 2 {
			t.Errorf("restartCount = %d, want 2", got)
		}
		if pod.Status.Phase != corev1.PodRunning {
			t.Errorf("phase after restarts = %s, want Running", pod.Status.Phase)
		}
	})

	t.Run("DeletePod recreates the pod with a new UID and the same binding", func(t *testing.T) {
		pool := e.createPool(t, "skua-replaced")
		e.binder.SetPoolReady(pool, 1)
		e.createClaim(t, "run-replaced", pool.Name)
		claim := e.waitReady(t, "run-replaced")
		podName := claim.Status.SandboxStatus.Name
		old, _ := e.pod(t, podName)
		e.binder.DeletePod(client.ObjectKey{Namespace: testNamespace, Name: podName})
		pod, ok := e.pod(t, podName)
		if !ok {
			t.Fatal("pod not recreated after DeletePod")
		}
		if pod.UID == old.UID {
			t.Errorf("recreated pod kept uid %s", old.UID)
		}
		assertBound(t, pod, claim)
		if pod.Labels["skua.dev/profile"] != "skua-replaced" {
			t.Errorf("recreated pod labels = %v, want the original labels", pod.Labels)
		}
		if pod.Status.Phase != corev1.PodRunning {
			t.Errorf("recreated pod phase = %s, want Running", pod.Status.Phase)
		}
	})

	t.Run("LosePod removes the pod and the claim is not rebound", func(t *testing.T) {
		pool := e.createPool(t, "skua-lost")
		e.binder.SetPoolReady(pool, 1)
		e.createClaim(t, "run-lost", pool.Name)
		claim := e.waitReady(t, "run-lost")
		podName := claim.Status.SandboxStatus.Name
		e.binder.LosePod(client.ObjectKey{Namespace: testNamespace, Name: podName})
		if _, ok := e.pod(t, podName); ok {
			t.Fatal("pod still exists after LosePod")
		}
		e.binder.SetPoolReady(pool, 1)
		e.clock.Step(time.Minute)
		claim.Labels = map[string]string{"touched": "true"}
		if err := e.c.Update(t.Context(), claim); err != nil {
			t.Fatalf("touch claim: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		var pods corev1.PodList
		if err := e.c.List(t.Context(), &pods, client.InNamespace(testNamespace),
			client.MatchingLabels{extv1beta1.SandboxIDLabel: string(claim.UID)}); err != nil {
			t.Fatalf("list pods: %v", err)
		}
		if len(pods.Items) != 0 {
			t.Errorf("claim was rebound to %s after LosePod", pods.Items[0].Name)
		}
	})

	t.Run("deleting the claim removes its pod", func(t *testing.T) {
		pool := e.createPool(t, "skua-delete")
		e.binder.SetPoolReady(pool, 1)
		claim := e.createClaim(t, "run-delete", pool.Name)
		bound := e.waitReady(t, "run-delete")
		podName := bound.Status.SandboxStatus.Name
		if err := e.c.Delete(t.Context(), claim); err != nil {
			t.Fatalf("delete claim: %v", err)
		}
		eventually(t, "pod "+podName+" deleted", func() bool {
			_, ok := e.pod(t, podName)
			return !ok
		})
	})
}
