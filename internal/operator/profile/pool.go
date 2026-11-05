package profile

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
)

// WarmPool renders the SandboxWarmPool of p in cfg.SandboxNamespace, owned by p and backed by its template.
func WarmPool(p *v1alpha1.AgentProfile, cfg *config.Config) *extv1beta1.SandboxWarmPool {
	return &extv1beta1.SandboxWarmPool{
		TypeMeta:   metav1.TypeMeta{APIVersion: extv1beta1.GroupVersion.String(), Kind: "SandboxWarmPool"},
		ObjectMeta: profileObjectMeta(p, cfg),
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    ptr.To(ptr.Deref(p.Spec.WarmPool.Replicas, 1)),
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: sandbox.Name(p.Name)},
		},
	}
}
