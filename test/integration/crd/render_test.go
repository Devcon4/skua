package crd_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/operator/profile"
	"github.com/Devcon4/skua/internal/operator/run"
	"github.com/Devcon4/skua/internal/testenv"
)

func TestRenderedObjectsValidateAgainstCRDs(t *testing.T) {
	_, c := testenv.Start(t)
	ctx := context.Background()
	cfg := testConfig()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.SandboxNamespace}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	objects := []client.Object{
		profile.Template(testProfile(), cfg),
		profile.Template(storageClassProfile(), cfg),
		profile.WarmPool(testProfile(), cfg),
	}
	for _, r := range testRuns() {
		objects = append(objects, run.Claim(r, cfg))
	}
	for _, obj := range objects {
		if err := c.Create(ctx, obj, client.DryRunAll); err != nil {
			t.Errorf("dry-run create %T %s: %v", obj, obj.GetName(), err)
		}
	}
}

func testConfig() *config.Config {
	return &config.Config{
		RunNamespace:     "skua",
		SandboxNamespace: "skua-sandboxes",
		Gateway: config.Gateway{
			BaseURL: "http://litellm.gateway.svc:4000",
			Egress:  config.EgressTarget{Namespace: "gateway", PodLabels: map[string]string{"app": "litellm"}, Port: 4000},
		},
		API: config.API{
			RunnerURL: "http://skua-api.skua.svc",
			Egress:    config.EgressTarget{Namespace: "skua", PodLabels: map[string]string{"app.kubernetes.io/name": "skua-api"}, Port: 8080},
		},
		GitHub: &config.GitHub{APIURL: "https://api.github.com", GitURL: "https://github.com"},
		Network: config.Network{
			PodCIDRs:     []string{"10.244.0.0/16"},
			ServiceCIDRs: []string{"10.96.0.0/16"},
			NodeCIDRs:    []string{"172.18.0.0/16", "172.19.0.2/32"},
			ExtraEgress: []config.EgressTarget{
				{Namespace: "skua-e2e", PodLabels: map[string]string{"app": "skua-e2e-forge"}, Port: 8080},
			},
		},
		Defaults: config.Defaults{RuntimeClassName: "gvisor"},
		Runner:   config.Runner{Image: "skua:dev", SupportedVersions: ">=0.1.0 <0.2.0"},
	}
}

func testProfile() *v1alpha1.AgentProfile {
	return &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "generic", UID: types.UID("0b6f3c1e-8a52-4d43-9b1a-6a3f7e0c2d11")},
		Spec: v1alpha1.AgentProfileSpec{
			Image:   "skua-generic:dev",
			Adapter: v1alpha1.AdapterOmpRPC,
			Command: []string{"omp", "--mode", "rpc", "--no-ui"},
			Models:  []string{"e2e-primary"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
			},
			Workspace: v1alpha1.ProfileWorkspace{Size: resource.MustParse("5Gi")},
			WarmPool:  v1alpha1.WarmPool{Replicas: ptr.To(int32(2))},
			Forge:     v1alpha1.ForgeGitHub,
			Limits: v1alpha1.Limits{
				BudgetUSD:      resource.MustParse("10"),
				MaxRuntime:     metav1.Duration{Duration: 4 * time.Hour},
				MaxRestarts:    ptr.To(int32(5)),
				MaxResultBytes: 4096,
			},
		},
	}
}

func storageClassProfile() *v1alpha1.AgentProfile {
	p := testProfile()
	p.Name = "pvc"
	p.Spec.Workspace.StorageClassName = ptr.To("standard")
	return p
}

func testRuns() map[string]*v1alpha1.AgentRun {
	base := func(name string) *v1alpha1.AgentRun {
		return &v1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "skua", UID: types.UID("uid-" + name)},
			Spec: v1alpha1.AgentRunSpec{
				Profile:   "generic",
				ClientKey: "key-" + name,
				Task:      v1alpha1.Task{Prompt: "do it"},
			},
		}
	}
	plain := base("generic-0000000001")
	withWorkspace := base("generic-0000000002")
	withWorkspace.Spec.Workspace = &v1alpha1.RunWorkspace{Repo: "e2e/sample", BaseRef: "main"}
	withSchema := base("generic-0000000003")
	withSchema.Spec.Task.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)}
	both := base("generic-0000000004")
	both.Spec.Workspace = &v1alpha1.RunWorkspace{Repo: "e2e/sample", BaseRef: "main"}
	both.Spec.Task.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)}
	return map[string]*v1alpha1.AgentRun{"plain": plain, "workspace": withWorkspace, "schema": withSchema, "workspace and schema": both}
}
