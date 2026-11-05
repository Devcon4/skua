package runner

import (
	"path/filepath"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

// Layout holds the pod paths the runner owns: BinDir holds skua-runner and the git credential helper,
// RunDir the runner's state and socket, WorkDir the agent's workspace, and TokenPath the service account token.
type Layout struct {
	BinDir, RunDir, WorkDir, TokenPath string
}

var defaultLayout = Layout{
	BinDir:    sandbox.BinDir,
	RunDir:    sandbox.RunDir,
	WorkDir:   sandbox.WorkDir,
	TokenPath: filepath.Join(sandbox.TokenDir, sandbox.TokenFile),
}

func (l Layout) statePath() string      { return filepath.Join(l.RunDir, "state.json") }
func (l Layout) sockPath() string       { return filepath.Join(l.RunDir, "runner.sock") }
func (l Layout) schemaPath() string     { return filepath.Join(l.RunDir, "schema.json") }
func (l Layout) forgeTokenPath() string { return filepath.Join(l.RunDir, "forge-token.json") }
func (l Layout) repoDir() string        { return filepath.Join(l.WorkDir, "repo") }
func (l Layout) homeDir() string        { return filepath.Join(l.WorkDir, "home") }
func (l Layout) agentDir() string       { return filepath.Join(l.WorkDir, "agent") }
func (l Layout) sessionDir() string     { return filepath.Join(l.WorkDir, "session") }
func (l Layout) mcpCommand() []string {
	return []string{filepath.Join(l.BinDir, sandbox.RunnerLink), "mcp"}
}
