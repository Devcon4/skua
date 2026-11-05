package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Adapter selects the protocol skua-runner speaks to the agent process.
// +kubebuilder:validation:Enum=omp-rpc
type Adapter string

const (
	AdapterOmpRPC Adapter = "omp-rpc"
)

// Forge selects the code host a run may push to.
// +kubebuilder:validation:Enum=github;none
type Forge string

const (
	ForgeGitHub Forge = "github"
	ForgeNone   Forge = "none"
)

// AgentProfileSpec describes the agent image, its models, its sandbox and the limits of every run that uses it.
type AgentProfileSpec struct {
	// Description is shown to developers when they list profiles.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`

	// Image is the agent container image, pinned by digest.
	// +kubebuilder:validation:MinLength=1
	// +required
	Image string `json:"image"`

	// Adapter is the protocol skua-runner uses to drive the agent.
	// +kubebuilder:default=omp-rpc
	// +optional
	Adapter Adapter `json:"adapter,omitempty"`

	// Command starts the agent inside the container; skua-runner runs it as its child.
	// +kubebuilder:validation:MinItems=1
	// +required
	Command []string `json:"command"`

	// Models are the gateway model names a run may use.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=128
	// +required
	Models []string `json:"models"`

	// RuntimeClassName overrides the installer's default runtime class for sandbox pods.
	// +optional
	RuntimeClassName *string `json:"runtimeClassName,omitempty"`

	// Resources are the agent container's requests and limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`

	// Workspace is the volume mounted at /work.
	// +kubebuilder:default={}
	// +optional
	Workspace ProfileWorkspace `json:"workspace,omitzero"`

	// WarmPool keeps idle sandboxes ready for new runs.
	// +kubebuilder:default={}
	// +optional
	WarmPool WarmPool `json:"warmPool,omitzero"`

	// Forge is the code host whose tokens a run receives; none gives the run no forge credentials.
	// +kubebuilder:default=none
	// +optional
	Forge Forge `json:"forge,omitempty"`

	// Limits bound every run of this profile.
	// +kubebuilder:default={}
	// +optional
	Limits Limits `json:"limits,omitzero"`
}

// ProfileWorkspace sizes the run's workspace volume.
type ProfileWorkspace struct {
	// Size is the workspace capacity.
	// +kubebuilder:default="10Gi"
	// +optional
	Size resource.Quantity `json:"size,omitzero"`

	// StorageClassName, when set, backs the workspace with a generic ephemeral volume of this class instead of an emptyDir.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// WarmPool sizes the profile's pool of idle sandboxes.
type WarmPool struct {
	// Replicas is the number of idle sandboxes kept ready.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
}

// Limits bound a single run.
type Limits struct {
	// BudgetUSD is the gateway spend limit of one run, in US dollars.
	// +kubebuilder:default="10"
	// +optional
	BudgetUSD resource.Quantity `json:"budgetUSD,omitzero"`

	// MaxRuntime is the longest a run may stay non-terminal, as a Go duration.
	// +kubebuilder:default="4h"
	// +optional
	MaxRuntime metav1.Duration `json:"maxRuntime,omitzero"`

	// MaxRestarts is how often skua-runner restarts a crashed agent before the run fails.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	// +optional
	MaxRestarts *int32 `json:"maxRestarts,omitempty"`

	// MaxResultBytes is the largest result a run may return.
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=1048576
	// +kubebuilder:default=262144
	// +optional
	MaxResultBytes int64 `json:"maxResultBytes,omitempty"`
}

// AgentProfileStatus reports whether the profile can serve runs.
type AgentProfileStatus struct {
	// Conditions holds the Ready condition.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// AgentProfile is a kind of agent developers can start runs of.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ap
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.warmPool.replicas`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 50",message="metadata.name must be at most 50 characters"
type AgentProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec AgentProfileSpec `json:"spec"`

	// +optional
	Status AgentProfileStatus `json:"status,omitempty"`
}

// AgentProfileList is a list of AgentProfile.
// +kubebuilder:object:root=true
type AgentProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentProfile `json:"items"`
}
