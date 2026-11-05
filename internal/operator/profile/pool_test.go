package profile

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

func TestTemplateAndPoolMetadata(t *testing.T) {
	p, cfg := testProfile(), testConfig()
	tpl, pool := Template(p, cfg), WarmPool(p, cfg)
	for _, meta := range []metav1.ObjectMeta{tpl.ObjectMeta, pool.ObjectMeta} {
		if meta.Name != "skua-generic" || meta.Namespace != "skua-sandboxes" {
			t.Errorf("object = %s/%s, want skua-sandboxes/skua-generic", meta.Namespace, meta.Name)
		}
		if meta.Labels[sandbox.LabelProfile] != "generic" {
			t.Errorf("labels = %v, want %s=generic", meta.Labels, sandbox.LabelProfile)
		}
		if len(meta.OwnerReferences) != 1 || meta.OwnerReferences[0].UID != p.UID || meta.OwnerReferences[0].Kind != "AgentProfile" ||
			meta.OwnerReferences[0].APIVersion != "skua.dev/v1alpha1" || !ptr.Deref(meta.OwnerReferences[0].Controller, false) {
			t.Errorf("owner references = %+v, want controller AgentProfile %s", meta.OwnerReferences, p.UID)
		}
	}
	if tpl.Spec.PodTemplate.ObjectMeta.Labels[sandbox.LabelProfile] != "generic" {
		t.Errorf("pod labels = %v", tpl.Spec.PodTemplate.ObjectMeta.Labels)
	}
	if pool.Spec.TemplateRef.Name != "skua-generic" || ptr.Deref(pool.Spec.Replicas, -1) != 2 {
		t.Errorf("pool spec = %+v", pool.Spec)
	}
}
