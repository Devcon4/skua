// Package runner is skua-runner: it holds one sandbox pod's run, drives the agent through an adapter and
// reports the agent's events and the run's result to skua-api.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Devcon4/skua/internal/runner/agent"
	"github.com/Devcon4/skua/internal/runner/apiclient"
	"github.com/Devcon4/skua/internal/runner/mcp"
	"github.com/Devcon4/skua/internal/runner/proc"
	"github.com/Devcon4/skua/internal/version"
)

const (
	killGrace      = 5 * time.Second
	killTimeout    = 30 * time.Second
	commandTimeout = 10 * time.Second
	exitTerminated = 143
)

// Main runs skua-runner for one sandbox pod with args `-- <agent command>` on the pod's paths, driving the
// agent through ad. It returns 143 after SIGTERM, 2 for missing args and 1 when the runner cannot start.
func Main(args []string, ad agent.Adapter) int {
	return MainLayout(args, defaultLayout, ad)
}

// MainLayout is Main with the runner's paths taken from l.
func MainLayout(args []string, l Layout, ad agent.Adapter) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: skua runner run -- <agent command> [args]")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	proc.StartReaper()
	s := settingsFromEnv(os.Getenv, os.Environ())
	client := apiclient.New(s.apiURL, l.TokenPath, l.RunDir, version.Version, nil)
	defer func() { _ = client.Close() }()
	return newRunner(l, s, args, ad, client).run(ctx)
}

type runner struct {
	l       Layout
	s       settings
	command []string
	ad      agent.Adapter
	client  *apiclient.Client
	hc      *http.Client

	lastRelay atomic.Int64
	relaying  atomic.Bool

	mu           sync.Mutex
	st           state
	driving      bool
	aborted      bool
	cancelDrive  context.CancelFunc
	agentRunning bool
	steers       []string
	driverDone   chan struct{}
}

func newRunner(l Layout, s settings, command []string, ad agent.Adapter, client *apiclient.Client) *runner {
	return &runner{l: l, s: s, command: command, ad: ad, client: client, hc: &http.Client{Timeout: commandTimeout}}
}

func (r *runner) run(ctx context.Context) int {
	if err := r.loadState(); err != nil {
		slog.Error("runner state unreadable, waiting for a new assignment", "path", r.l.statePath(), "err", err)
	}
	if err := os.MkdirAll(r.l.RunDir, 0o700); err != nil {
		slog.Error("runner state dir unavailable", "path", r.l.RunDir, "err", err)
		return 1
	}
	ln, err := mcp.Listen(r.l.sockPath())
	if err != nil {
		slog.Error("runner result socket unavailable", "path", r.l.sockPath(), "err", err)
		return 1
	}
	defer func() { _ = ln.Close() }()
	go r.acceptSubmissions(ln)
	stopRelay := r.startRelay()
	r.mu.Lock()
	if r.st.Assign != nil {
		r.client.SetRun(r.st.Assign.Run)
	}
	r.client.SetLastEventID(r.st.LastCommand)
	resume := r.st.Assign != nil && !r.st.Finished
	r.mu.Unlock()
	cmds := r.client.Commands(ctx)
	if resume {
		r.startDriver(ctx, true)
	}
	for {
		select {
		case <-ctx.Done():
			return r.terminate(stopRelay)
		case c, ok := <-cmds:
			if !ok {
				return r.terminate(stopRelay)
			}
			r.handle(ctx, c)
			r.update(func(st *state) { st.LastCommand = c.ID })
		}
	}
}

func (r *runner) terminate(stopRelay func(deadline context.Context)) int {
	r.killTree(context.Background())
	r.mu.Lock()
	driverDone := r.driverDone
	r.mu.Unlock()
	if driverDone != nil {
		<-driverDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), killGrace)
	defer cancel()
	if err := r.ad.Drain(ctx); err != nil {
		slog.Warn("agent events not all relayed before shutdown", "err", err)
	}
	stopRelay(ctx)
	return exitTerminated
}

func (r *runner) killTree(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), killTimeout)
	defer cancel()
	if err := proc.KillTree(ctx, killGrace); err != nil {
		slog.Error("runner could not kill the agent's processes", "err", err)
	}
}
