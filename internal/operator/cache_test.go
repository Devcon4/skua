package operator

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

func TestCacheOptionsNamespaces(t *testing.T) {
	cfg := operatorTestConfig(t)
	opts := CacheOptions(cfg)
	if len(opts.DefaultNamespaces) != 0 {
		t.Errorf("DefaultNamespaces = %v, want none so every namespaced type is scoped in ByObject", slices.Collect(maps.Keys(opts.DefaultNamespaces)))
	}

	got := map[string][]string{}
	for obj, by := range opts.ByObject {
		got[fmt.Sprintf("%T", obj)] = slices.Sorted(maps.Keys(by.Namespaces))
	}
	want := map[string][]string{
		"*v1alpha1.AgentRun":       {"skua"},
		"*v1.Secret":               {"skua"},
		"*v1beta1.SandboxTemplate": {"skua-sandboxes"},
		"*v1beta1.SandboxWarmPool": {"skua-sandboxes"},
		"*v1beta1.SandboxClaim":    {"skua-sandboxes"},
		"*v1beta1.Sandbox":         {"skua-sandboxes"},
		"*v1.Pod":                  {"skua-sandboxes"},
	}
	for typ, ns := range want {
		if !slices.Equal(got[typ], ns) {
			t.Errorf("%s cached in %v, want %v", typ, got[typ], ns)
		}
	}
	for typ, ns := range got {
		if _, ok := want[typ]; !ok {
			t.Errorf("%s cached in %v, but no RBAC grant is listed for it", typ, ns)
		}
	}

	for obj, by := range opts.ByObject {
		if _, ok := obj.(*corev1.Secret); ok {
			if by.Label == nil || by.Label.String() != sandbox.LabelRunSecret+"=true" {
				t.Errorf("Secret cache label selector = %v, want %s=true", by.Label, sandbox.LabelRunSecret)
			}
		}
	}
}
