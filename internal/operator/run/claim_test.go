package run

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
)

func TestClaimHoldsOnlyWarmPoolRef(t *testing.T) {
	cfg := &config.Config{RunNamespace: "skua", SandboxNamespace: "skua-sandboxes"}
	want := extv1beta1.SandboxClaimSpec{WarmPoolRef: extv1beta1.SandboxWarmPoolRef{Name: "skua-generic"}}
	for name, run := range testRuns() {
		t.Run(name, func(t *testing.T) {
			c := Claim(run, cfg)
			if !reflect.DeepEqual(c.Spec, want) {
				t.Errorf("spec = %+v, want %+v", c.Spec, want)
			}
			if c.Name != run.Name || c.Namespace != "skua-sandboxes" {
				t.Errorf("claim = %s/%s, want skua-sandboxes/%s", c.Namespace, c.Name, run.Name)
			}
			wantLabels := map[string]string{sandbox.LabelRunName: run.Name, sandbox.LabelRunUID: string(run.UID), sandbox.LabelProfile: "generic"}
			if !reflect.DeepEqual(c.Labels, wantLabels) {
				t.Errorf("labels = %v, want %v", c.Labels, wantLabels)
			}
			if len(c.OwnerReferences) != 0 {
				t.Errorf("owner references = %v, want none", c.OwnerReferences)
			}
		})
	}
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
