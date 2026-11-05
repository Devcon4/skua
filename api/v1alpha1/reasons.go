package v1alpha1

// Reasons of a terminal AgentRun, in status.reason.
const (
	ReasonCompleted                = "Completed"
	ReasonCancelled                = "Cancelled"
	ReasonMaxRuntime               = "MaxRuntime"
	ReasonBudgetExceeded           = "BudgetExceeded"
	ReasonCrashLoop                = "CrashLoop"
	ReasonPodLost                  = "PodLost"
	ReasonKeyRevoked               = "KeyRevoked"
	ReasonModelRefused             = "ModelRefused"
	ReasonModelNotFound            = "ModelNotFound"
	ReasonAgentProtocolUnsupported = "AgentProtocolUnsupported"
	ReasonAgentStartFailed         = "AgentStartFailed"
	ReasonAgentError               = "AgentError"
	ReasonResultTooLarge           = "ResultTooLarge"
	ReasonResultInvalid            = "ResultInvalid"
	ReasonWorkspaceError           = "WorkspaceError"
	ReasonRunnerUnsupported        = "RunnerUnsupported"
	ReasonProfileNotFound          = "ProfileNotFound"
	ReasonProfileNotReady          = "ProfileNotReady"
	ReasonGatewayError             = "GatewayError"
	ReasonForgeError               = "ForgeError"
)

// Reasons of an AgentProfile's Ready condition.
const (
	ReasonModelUnknown            = "ModelUnknown"
	ReasonModelUnpriced           = "ModelUnpriced"
	ReasonGatewayUnavailable      = "GatewayUnavailable"
	ReasonPoolNotStarting         = "PoolNotStarting"
	ReasonAgentSandboxUnsupported = "AgentSandboxUnsupported"
	ReasonReady                   = "Ready"
)
