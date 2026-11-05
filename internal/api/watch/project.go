package watch

import (
	"context"
	"encoding/json"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func (w *watcher) runChanged(prev, run *v1alpha1.AgentRun) {
	if prev.Status.Phase == run.Status.Phase {
		return
	}
	hub := w.deps.Hub
	status, err := json.Marshal(wire.StatusData{Phase: string(run.Status.Phase), Reason: run.Status.Reason})
	if err != nil {
		logger.Error(err, "encode status event", "run", run.Name)
		return
	}
	hub.AppendEvent(run.Name, wire.Event{Type: wire.EventStatus, Data: status})
	switch {
	case run.Status.Phase.Terminal():
		if data, err := json.Marshal(agentrun.EndData(run)); err != nil {
			logger.Error(err, "encode end event", "run", run.Name)
		} else {
			hub.AppendEvent(run.Name, wire.Event{Type: wire.EventEnd, Data: data})
		}
		hub.Forget(run.Name)
		w.deps.Runner.ForgetRun(run.Name)
	case run.Status.Phase == v1alpha1.PhaseRunning:
		w.withTimeout(func(ctx context.Context) { w.deps.Runner.Assign(ctx, run.Status.PodName) })
	}
}
