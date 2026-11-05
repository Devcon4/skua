package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// RunPhase is the lifecycle phase of a run; Succeeded and Failed are terminal.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type RunPhase string

const (
	PhasePending   RunPhase = "Pending"
	PhaseRunning   RunPhase = "Running"
	PhaseSucceeded RunPhase = "Succeeded"
	PhaseFailed    RunPhase = "Failed"
)

// Terminal reports whether p is Succeeded or Failed.
func (p RunPhase) Terminal() bool {
	return p == PhaseSucceeded || p == PhaseFailed
}

// AgentRunSpec is the task a developer asked for. It is immutable.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type AgentRunSpec struct {
	// Profile names the AgentProfile the run uses.
	// +kubebuilder:validation:MinLength=1
	// +required
	Profile string `json:"profile"`

	// ClientKey is chosen by the client so a retried create returns the existing run.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +required
	ClientKey string `json:"clientKey"`

	// Workspace is the repository cloned into /work; omitted for runs without a repository.
	// +optional
	Workspace *RunWorkspace `json:"workspace,omitempty"`

	// Task is the prompt and the optional result schema.
	// +required
	Task Task `json:"task"`
}

// RunWorkspace names the repository and ref a run starts from.
type RunWorkspace struct {
	// Repo is the repository as owner/name.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`
	// +required
	Repo string `json:"repo"`

	// BaseRef is the branch, tag or commit the workspace is checked out at.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +required
	BaseRef string `json:"baseRef"`
}

// Task is what the agent is asked to do.
type Task struct {
	// Prompt is the instruction sent to the agent.
	// +kubebuilder:validation:MinLength=1
	// +required
	Prompt string `json:"prompt"`

	// Schema is a JSON Schema the result must satisfy; when set the agent returns its result through submit_result.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Schema *runtime.RawExtension `json:"schema,omitempty"`
}

// RunOutputs are the forge objects a run produced.
type RunOutputs struct {
	// Branch is the pushed branch.
	// +optional
	Branch string `json:"branch,omitempty"`

	// PullRequest is the number of the opened pull request.
	// +optional
	PullRequest int64 `json:"pullRequest,omitempty"`
}

// AgentRunStatus is the observed state of a run. Phase and reason never change once the phase is terminal.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.phase) || !(oldSelf.phase in ['Succeeded', 'Failed']) || (has(self.phase) && self.phase == oldSelf.phase && has(self.reason) == has(oldSelf.reason) && (!has(self.reason) || self.reason == oldSelf.reason))",message="phase and reason are immutable once the phase is terminal"
type AgentRunStatus struct {
	// Phase is Pending, Running, Succeeded or Failed.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// Reason is set when the phase turns terminal.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message carries detail on the reason.
	// +optional
	Message string `json:"message,omitempty"`

	// Claim is the name of the run's SandboxClaim.
	// +optional
	Claim string `json:"claim,omitempty"`

	// PodName is the sandbox pod bound to the claim.
	// +optional
	PodName string `json:"podName,omitempty"`

	// PodUID is the uid of that pod.
	// +optional
	PodUID types.UID `json:"podUID,omitempty"`

	// StartedAt is when the run turned Running.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is when the run turned terminal.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// ProfileSnapshot is the profile spec the run was admitted with.
	// +optional
	ProfileSnapshot *AgentProfileSpec `json:"profileSnapshot,omitempty"`

	// Result is the agent's final text as a string, or the JSON value it submitted.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Result *runtime.RawExtension `json:"result,omitempty"`

	// Outputs are the forge objects the run produced.
	// +optional
	Outputs *RunOutputs `json:"outputs,omitempty"`

	// Conditions holds additional run conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// AgentRun is one task executed by an agent in a sandbox.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ar
// +kubebuilder:printcolumn:name="Profile",type=string,JSONPath=`.spec.profile`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec AgentRunSpec `json:"spec"`

	// +optional
	Status AgentRunStatus `json:"status,omitempty"`
}

// AgentRunList is a list of AgentRun.
// +kubebuilder:object:root=true
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}
