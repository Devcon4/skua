package sandbox

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
)

func TestRunForPod(t *testing.T) {
	cfg := &config.Config{RunNamespace: "skua", SandboxNamespace: "skua-sandboxes"}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme, extv1beta1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("build scheme: %v", err)
		}
	}

	tests := []struct {
		name   string
		mutate func(*corev1.Pod, *extv1beta1.SandboxClaim, *v1alpha1.AgentRun)
		want   bool
	}{
		{name: "matching pod, claim and run", want: true},
		{name: "pod claim uid differs from claim", mutate: func(p *corev1.Pod, _ *extv1beta1.SandboxClaim, _ *v1alpha1.AgentRun) {
			p.Labels[extv1beta1.SandboxIDLabel] = "claim-uid-other"
		}},
		{name: "claim run uid differs from run", mutate: func(_ *corev1.Pod, c *extv1beta1.SandboxClaim, _ *v1alpha1.AgentRun) {
			c.Labels[LabelRunUID] = "run-uid-other"
		}},
		{name: "run pod uid differs from pod", mutate: func(_ *corev1.Pod, _ *extv1beta1.SandboxClaim, r *v1alpha1.AgentRun) {
			r.Status.PodUID = "pod-uid-other"
		}},
		{name: "claim has no run label", mutate: func(_ *corev1.Pod, c *extv1beta1.SandboxClaim, _ *v1alpha1.AgentRun) {
			delete(c.Labels, LabelRunName)
		}},
		{name: "pod has no claim annotation", mutate: func(p *corev1.Pod, _ *extv1beta1.SandboxClaim, _ *v1alpha1.AgentRun) {
			delete(p.Annotations, extv1beta1.SandboxClaimNameAnnotation)
		}},
		{name: "claim missing", mutate: func(p *corev1.Pod, _ *extv1beta1.SandboxClaim, _ *v1alpha1.AgentRun) {
			p.Annotations[extv1beta1.SandboxClaimNameAnnotation] = "lookup-other"
		}},
		{name: "run missing", mutate: func(_ *corev1.Pod, c *extv1beta1.SandboxClaim, _ *v1alpha1.AgentRun) {
			c.Labels[LabelRunName] = "lookup-other"
		}},
		{name: "run outside the run namespace", mutate: func(_ *corev1.Pod, _ *extv1beta1.SandboxClaim, r *v1alpha1.AgentRun) {
			r.Namespace = "elsewhere"
		}},
		{name: "pod outside the sandbox namespace", mutate: func(p *corev1.Pod, _ *extv1beta1.SandboxClaim, _ *v1alpha1.AgentRun) {
			p.Namespace = cfg.RunNamespace
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := &v1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{Namespace: cfg.RunNamespace, Name: "lookup", UID: "run-uid"},
				Status:     v1alpha1.AgentRunStatus{Phase: v1alpha1.PhaseRunning, PodName: "lookup", PodUID: "pod-uid"},
			}
			claim := &extv1beta1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{
				Namespace: cfg.SandboxNamespace, Name: "lookup", UID: "claim-uid",
				Labels: map[string]string{LabelRunName: "lookup", LabelRunUID: "run-uid"},
			}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: cfg.SandboxNamespace, Name: "lookup", UID: "pod-uid",
				Labels:      map[string]string{extv1beta1.SandboxIDLabel: "claim-uid"},
				Annotations: map[string]string{extv1beta1.SandboxClaimNameAnnotation: "lookup"},
			}}
			if tt.mutate != nil {
				tt.mutate(pod, claim, run)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim, run).Build()

			got, err := RunForPod(t.Context(), c, c, cfg.SandboxNamespace, cfg.RunNamespace, pod)
			if err != nil {
				t.Fatalf("lookup: %v", err)
			}
			if tt.want != (got != nil) {
				t.Fatalf("lookup returned run %v, want found=%v", got, tt.want)
			}
			if got != nil && got.UID != run.UID {
				t.Errorf("lookup returned run uid %s, want %s", got.UID, run.UID)
			}
		})
	}
}
