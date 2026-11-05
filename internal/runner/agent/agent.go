// Package agent defines the contract between the runner and the adapter that drives
// one agent process.
package agent

import (
	"context"

	"github.com/Devcon4/skua/internal/contract/wire"
)

// Adapter drives one agent process for one run.
type Adapter interface {
	// Start spawns the agent, completes the handshake and opens the session; p.Resume
	// reopens the existing session after a restart.
	Start(ctx context.Context, p StartParams) error
	// Prompt sends one prompt and blocks until the turn settles.
	Prompt(ctx context.Context, text string) (TurnResult, error)
	Steer(ctx context.Context, text string) error
	Abort(ctx context.Context) error
	LastText(ctx context.Context) (string, error)
	Events() <-chan wire.Event
	// Exited yields once when the agent process ends.
	Exited() <-chan error
	Stop(ctx context.Context) error
}

type StartParams struct {
	Command                       []string
	Env                           []string
	WorkDir, AgentDir, SessionDir string
	GatewayBaseURL, GatewayKey    string
	Models                        []string
	MCP                           MCPConfig
	Resume                        bool
}

type MCPConfig struct {
	Command                []string
	SocketPath, SchemaFile string
}

// TurnResult is how a prompt ended. Status is completed, aborted or error; Reason is
// the AgentRun reason for an error and empty otherwise.
type TurnResult struct {
	Status          string
	Reason, Message string
}

// Error is an adapter failure carrying the AgentRun reason it maps to.
type Error struct {
	Reason, Message string
}

func (e *Error) Error() string { return e.Reason + ": " + e.Message }
