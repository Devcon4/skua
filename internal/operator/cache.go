package operator

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
)

// CacheOptions scopes the manager cache to the namespaces the operator's Roles grant: AgentRuns and
// run Secrets in cfg.RunNamespace, agent-sandbox objects and pods in cfg.SandboxNamespace. A namespaced
// type missing from ByObject is listed cluster-wide, which the operator's Roles forbid.
func CacheOptions(cfg *config.Config) cache.Options {
	runNS := map[string]cache.Config{cfg.RunNamespace: {}}
	sandboxNS := map[string]cache.Config{cfg.SandboxNamespace: {}}
	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.AgentRun{}: {Namespaces: runNS},
			&corev1.Secret{}: {
				Namespaces: runNS,
				Label:      labels.SelectorFromSet(labels.Set{sandbox.LabelRunSecret: "true"}),
			},
			&extv1beta1.SandboxTemplate{}: {Namespaces: sandboxNS},
			&extv1beta1.SandboxWarmPool{}: {Namespaces: sandboxNS},
			&extv1beta1.SandboxClaim{}:    {Namespaces: sandboxNS},
			&sandboxv1beta1.Sandbox{}:     {Namespaces: sandboxNS},
			&corev1.Pod{}:                 {Namespaces: sandboxNS},
		},
	}
}
