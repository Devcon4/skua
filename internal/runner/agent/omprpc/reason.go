package omprpc

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/runner/agent"
)

func startFailed(err error) *agent.Error {
	return &agent.Error{Reason: v1alpha1.ReasonAgentStartFailed, Message: err.Error()}
}

func turnResult(r promptResult) agent.TurnResult {
	switch r.Status {
	case "completed", "aborted":
		return agent.TurnResult{Status: r.Status}
	}
	if r.Error == nil {
		return agent.TurnResult{Status: "error", Reason: v1alpha1.ReasonAgentError, Message: "prompt ended with status " + strconv.Quote(r.Status)}
	}
	return agent.TurnResult{Status: "error", Reason: errorReason(r.Error.HTTPStatus, r.Error.Message), Message: r.Error.Message}
}

var errorTypePattern = regexp.MustCompile(`(?:\btype=|"type"\s*:\s*")([A-Za-z0-9_]+)`)

func errorReason(status int, message string) string {
	typ := ""
	if m := errorTypePattern.FindStringSubmatch(message); m != nil {
		typ = m[1]
	}
	switch {
	case status == http.StatusUnprocessableEntity && typ == "budget_exceeded":
		return v1alpha1.ReasonBudgetExceeded
	case status == http.StatusUnauthorized && typ == "expired_key":
		return v1alpha1.ReasonMaxRuntime
	case status == http.StatusUnauthorized && typ == "token_not_found_in_db":
		return v1alpha1.ReasonKeyRevoked
	case status == http.StatusForbidden && typ == "key_model_access_denied":
		return v1alpha1.ReasonModelRefused
	}
	return v1alpha1.ReasonAgentError
}
