package runner

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/gitcred"
)

func TestRunnerAgentEnv(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	e.start(f, e.settings())
	a := plainAssign()
	a.Forge = &wire.Forge{Token: "ghs_secret_value", ExpiresAt: time.Now().Add(time.Hour)}
	e.send(wire.CommandAssign, a)
	e.result()
	p := f.snapshot().starts[0]
	want := map[string]string{
		"HOME":                    e.l.homeDir(),
		"GIT_CONFIG_COUNT":        "3",
		"GIT_CONFIG_KEY_0":        "credential.helper",
		"GIT_CONFIG_VALUE_0":      "skua",
		"GIT_CONFIG_KEY_1":        "user.name",
		"GIT_CONFIG_VALUE_1":      "skua",
		"GIT_CONFIG_KEY_2":        "user.email",
		"GIT_CONFIG_VALUE_2":      "skua@skua.invalid",
		sandbox.EnvForgeTokenFile: e.l.forgeTokenPath(),
		"SKUA_RUN":                testRun,
		"KEEP":                    "1",
	}
	for k, v := range want {
		if got, n := envValue(p.Env, k); got != v || n != 1 {
			t.Errorf("env %s = %q (%d entries), want %q once", k, got, n, v)
		}
	}
	if path, n := envValue(p.Env, "PATH"); n != 1 || !strings.HasPrefix(path, e.l.BinDir+":") {
		t.Errorf("PATH = %q (%d entries), want %s first", path, n, e.l.BinDir)
	}
	if _, n := envValue(p.Env, "SKUA_REPO"); n != 0 {
		t.Errorf("SKUA_REPO set without a workspace")
	}
	for _, kv := range p.Env {
		if strings.Contains(kv, "ghs_secret_value") {
			t.Errorf("forge token in agent env: %s", kv)
		}
	}
	if tok, err := gitcred.ReadToken(e.l.forgeTokenPath()); err != nil || tok.Token != "ghs_secret_value" {
		t.Errorf("forge token file = %+v, %v", tok, err)
	}
	if p.Resume || p.WorkDir != e.l.repoDir() || p.AgentDir != e.l.agentDir() || p.SessionDir != e.l.sessionDir() || p.GatewayKey != "sk-skua-test" || p.MCP.SchemaFile != "" {
		t.Errorf("start params = %+v", p)
	}
	if !slices.Equal(p.MCP.Command, []string{filepath.Join(e.l.BinDir, "skua-runner"), "mcp"}) || p.MCP.SocketPath != e.l.sockPath() {
		t.Errorf("mcp config = %+v", p.MCP)
	}
}

func envValue(env []string, key string) (string, int) {
	val, n := "", 0
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			val = v
			n++
		}
	}
	return val, n
}
