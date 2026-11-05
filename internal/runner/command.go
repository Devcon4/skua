package runner

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func (r *runner) handle(ctx context.Context, c wire.Command) {
	switch c.Type {
	case wire.CommandAssign:
		var a wire.Assign
		if err := json.Unmarshal(c.Data, &a); err != nil || a.Run == "" {
			slog.Error("runner ignored an invalid assign", "id", c.ID, "err", err)
			return
		}
		r.assign(ctx, a)
	case wire.CommandSteer:
		var s wire.Steer
		if err := json.Unmarshal(c.Data, &s); err != nil {
			slog.Error("runner ignored an invalid steer", "id", c.ID, "err", err)
			return
		}
		r.steer(ctx, s.Message)
	case wire.CommandAbort:
		r.abort()
	case wire.CommandToken:
		var t wire.Token
		if err := json.Unmarshal(c.Data, &t); err != nil || t.Token == "" {
			slog.Error("runner ignored an invalid token command", "id", c.ID, "err", err)
			return
		}
		if err := r.writeForgeToken(t.Token, t.ExpiresAt); err != nil {
			slog.Error("runner could not write the forge token", "err", err)
		}
	default:
		slog.Warn("runner ignored an unknown command", "id", c.ID, "type", c.Type)
	}
}

func (r *runner) assign(ctx context.Context, a wire.Assign) {
	r.mu.Lock()
	if cur := r.st.Assign; cur != nil {
		r.mu.Unlock()
		if cur.Run != a.Run {
			slog.Warn("runner ignored an assign for another run", "current", cur.Run, "run", a.Run)
		}
		return
	}
	r.st = state{Assign: &a, LastCommand: r.st.LastCommand}
	err := r.saveLocked()
	r.mu.Unlock()
	if err != nil {
		slog.Error("runner could not persist the assignment", "run", a.Run, "err", err)
	}
	r.startDriver(ctx, false)
}

func (r *runner) steer(ctx context.Context, msg string) {
	r.mu.Lock()
	if !r.driving || r.aborted {
		r.mu.Unlock()
		slog.Info("runner ignored a steer without an active run")
		return
	}
	if !r.agentRunning {
		r.steers = append(r.steers, msg)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.sendSteer(ctx, msg)
}

func (r *runner) sendSteer(ctx context.Context, msg string) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if err := r.ad.Steer(ctx, msg); err != nil {
		slog.Warn("runner could not steer the agent", "err", err)
	}
}

func (r *runner) abort() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.driving || r.aborted {
		return
	}
	r.aborted = true
	r.cancelDrive()
}

func (r *runner) isAborted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.aborted
}
