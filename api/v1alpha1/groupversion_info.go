// Package v1alpha1 holds the skua.dev v1alpha1 API types AgentProfile and AgentRun.
// +kubebuilder:object:generate=true
// +groupName=skua.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version of every type in this package.
	GroupVersion = schema.GroupVersion{Group: "skua.dev", Version: "v1alpha1"}

	// SchemeBuilder registers the types of this package.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types of this package to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&AgentProfile{}, &AgentProfileList{},
		&AgentRun{}, &AgentRunList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
