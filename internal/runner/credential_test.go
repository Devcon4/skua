package runner

import (
	"testing"

	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func TestRunnerCredentialHelperScopedToForge(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	s := e.settings()
	s.forgeGitURL = "https://forge.example/"
	e.start(f, s)
	e.send(wire.CommandAssign, plainAssign())
	e.result()
	env := f.snapshot().starts[0].Env
	if got, n := envValue(env, "GIT_CONFIG_KEY_0"); got != "credential.https://forge.example.helper" || n != 1 {
		t.Errorf("GIT_CONFIG_KEY_0 = %q (%d entries)", got, n)
	}
	if got, n := envValue(env, sandbox.EnvForgeGitURL); got != "https://forge.example/" || n != 1 {
		t.Errorf("%s = %q (%d entries)", sandbox.EnvForgeGitURL, got, n)
	}
}
