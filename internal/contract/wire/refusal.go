package wire

// Reasons skua-api gives when it refuses a runner request.
const (
	RefusalTokenInvalid           = "TokenInvalid"
	RefusalPodMismatch            = "PodMismatch"
	RefusalRunMismatch            = "RunMismatch"
	RefusalRunFinished            = "RunFinished"
	RefusalStreamActive           = "StreamActive"
	RefusalBadRequest             = "BadRequest"
	RefusalBodyTooLarge           = "BodyTooLarge"
	RefusalEventLogFull           = "EventLogFull"
	RefusalInternalError          = "InternalError"
	RefusalTokenReviewUnavailable = "TokenReviewUnavailable"
)
