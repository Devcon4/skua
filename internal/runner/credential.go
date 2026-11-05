package runner

import (
	"strings"
	"time"

	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/runner/gitcred"
)

// credentialHelperKey scopes the helper to the forge so git never asks it for another host's credentials.
func (r *runner) credentialHelperKey() string {
	if r.s.forgeGitURL == "" {
		return "credential.helper"
	}
	return "credential." + strings.TrimSuffix(r.s.forgeGitURL, "/") + ".helper"
}

func (r *runner) gitCredentialEnv() []string {
	return []string{
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=" + r.credentialHelperKey(),
		"GIT_CONFIG_VALUE_0=skua",
		"GIT_CONFIG_KEY_1=user.name",
		"GIT_CONFIG_VALUE_1=skua",
		"GIT_CONFIG_KEY_2=user.email",
		"GIT_CONFIG_VALUE_2=skua@skua.invalid",
		sandbox.EnvForgeTokenFile + "=" + r.l.forgeTokenPath(),
	}
}

func (r *runner) writeForgeToken(token string, expiresAt time.Time) error {
	return gitcred.WriteToken(r.l.forgeTokenPath(), gitcred.Token{Token: token, ExpiresAt: expiresAt})
}

func (r *runner) forgeToken() (gitcred.Token, error) {
	return gitcred.ReadToken(r.l.forgeTokenPath())
}
