package health

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
)

func TestRunHealthPodReadErrorReturnsNoRequeueAfter(t *testing.T) {
	cfg := &config.Config{RunNamespace: "skua", SandboxNamespace: "skua-sandboxes"}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("build scheme: %v", err)
		}
	}
	clk := clocktesting.NewFakeClock(time.Now())
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: cfg.RunNamespace, Name: "read-error", CreationTimestamp: metav1.NewTime(clk.Now())},
		Status: v1alpha1.AgentRunStatus{
			Phase:           v1alpha1.PhaseRunning,
			PodName:         "read-error",
			PodUID:          "pod-uid",
			ProfileSnapshot: &v1alpha1.AgentProfileSpec{Limits: v1alpha1.Limits{MaxRuntime: metav1.Duration{Duration: time.Hour}}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
	reader := interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Pod); ok {
				return errors.New("apiserver unavailable")
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	r := &reconciler{client: c, apiReader: reader, cfg: cfg, clock: clk}

	res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err == nil {
		t.Fatal("reconcile succeeded while the pod read failed")
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %s with error %v, want 0", res.RequeueAfter, err)
	}
}
