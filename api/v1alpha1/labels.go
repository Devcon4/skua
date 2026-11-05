package v1alpha1

const (
	// LabelOwner on an AgentRun names the user that created it.
	LabelOwner = "skua.dev/owner"
	// AnnotationFetchedAt on a terminal AgentRun records, in RFC 3339, when its owner first fetched the result.
	AnnotationFetchedAt = "skua.dev/fetched-at"
	// ConditionReady is the condition type that reports whether an AgentProfile can serve runs.
	ConditionReady = "Ready"
)
