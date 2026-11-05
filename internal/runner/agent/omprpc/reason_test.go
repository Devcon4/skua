package omprpc

import (
	"testing"

	"github.com/Devcon4/skua/api/v1alpha1"
)

func TestPromptErrorReasons(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"})
	h.mustStart()
	for _, tc := range []struct{ script, reason string }{
		{"SCRIPT:error 422 budget_exceeded", v1alpha1.ReasonBudgetExceeded},
		{"SCRIPT:error 401 expired_key", v1alpha1.ReasonMaxRuntime},
		{"SCRIPT:error 401 token_not_found_in_db", v1alpha1.ReasonKeyRevoked},
		{"SCRIPT:error 403 key_model_access_denied", v1alpha1.ReasonModelRefused},
		{"SCRIPT:error 500 internal_server_error", v1alpha1.ReasonAgentError},
	} {
		r := h.prompt(tc.script)
		if r.Status != "error" || r.Reason != tc.reason || r.Message == "" {
			t.Errorf("%s: turn = %+v, want reason %s", tc.script, r, tc.reason)
		}
	}
}
