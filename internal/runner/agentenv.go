package runner

import (
	"strings"

	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
)

func (r *runner) startParams(a wire.Assign, resume bool) agent.StartParams {
	schema := ""
	if len(a.Task.Schema) > 0 {
		schema = r.l.schemaPath()
	}
	return agent.StartParams{
		Command:        r.command,
		Env:            r.agentEnv(a),
		WorkDir:        r.l.repoDir(),
		AgentDir:       r.l.agentDir(),
		SessionDir:     r.l.sessionDir(),
		GatewayBaseURL: a.Gateway.BaseURL,
		GatewayKey:     a.Gateway.Key,
		Models:         a.Gateway.Models,
		MCP:            agent.MCPConfig{Command: r.l.mcpCommand(), SocketPath: r.l.sockPath(), SchemaFile: schema},
		Resume:         resume,
	}
}

// agentEnv is the container environment with the run's paths, git identity and credential helper. It never
// carries the forge token, only the file the helper reads it from.
func (r *runner) agentEnv(a wire.Assign) []string {
	path := r.l.BinDir
	for _, kv := range r.s.environ {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok && v != "" {
			path += ":" + v
		}
	}
	set := append([]string{"HOME=" + r.l.homeDir(), "PATH=" + path}, r.gitCredentialEnv()...)
	set = append(set, sandbox.EnvRun+"="+a.Run)
	if r.s.forgeGitURL != "" {
		set = append(set, sandbox.EnvForgeGitURL+"="+r.s.forgeGitURL)
	}
	if a.Workspace != nil {
		set = append(set, sandbox.EnvRepo+"="+a.Workspace.Repo)
	}
	return mergeEnv(r.s.environ, set)
}

func mergeEnv(base, set []string) []string {
	keys := make(map[string]bool, len(set))
	for _, kv := range set {
		k, _, _ := strings.Cut(kv, "=")
		keys[k] = true
	}
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !keys[k] {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}
