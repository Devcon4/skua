package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/runner"
	"github.com/Devcon4/skua/internal/runner/agent"
	"github.com/Devcon4/skua/internal/runner/agent/omprpc"
	"github.com/Devcon4/skua/internal/runner/mcp"
)

const runnerUsage = `usage: skua runner <command>

commands:
  run -- <agent command> [args]   run the agent for this pod's run
  mcp                             serve the skua MCP server on stdio
  install <dir>                   install skua and its links into dir
`

func init() {
	register("runner", runnerMain)
}

func runnerMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, runnerUsage)
		return 2
	}
	switch args[0] {
	case "run":
		return runnerRun(args[1:])
	case "mcp":
		return runnerMCP(args[1:])
	case "install":
		return runnerInstall(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "skua runner: unknown command %q\n%s", args[0], runnerUsage)
		return 2
	}
}

func runnerRun(args []string) int {
	ad, err := newAdapter(os.Getenv(sandbox.EnvAdapter))
	if err != nil {
		slog.Error("runner cannot start", "err", err)
		return 1
	}
	return runner.Main(args, ad)
}

// newAdapter returns the adapter for a profile's adapter name. Only omp-rpc exists; any other name is an error.
func newAdapter(name string) (agent.Adapter, error) {
	switch v1alpha1.Adapter(name) {
	case v1alpha1.AdapterOmpRPC:
		return omprpc.New(), nil
	default:
		return nil, fmt.Errorf("runner: unknown adapter %q", name)
	}
}

func runnerMCP(args []string) int {
	if len(args) > 0 {
		fmt.Fprint(os.Stderr, runnerUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := mcp.Serve(ctx, os.Stdin, os.Stdout, os.Getenv); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "skua runner mcp:", err)
		return 1
	}
	return 0
}

func runnerInstall(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, runnerUsage)
		return 2
	}
	if err := runner.Install(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "skua runner install:", err)
		return 1
	}
	return 0
}
