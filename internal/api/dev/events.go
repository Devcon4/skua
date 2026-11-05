package dev

import (
	"encoding/json"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func (d *routes) events(w http.ResponseWriter, r *http.Request) {
	run, ok := d.ownedRun(w, r)
	if !ok {
		return
	}
	lastID, _ := wire.LastEventID(r)
	backlog, live, cancel := d.hub.Subscribe(run.Name, lastID)
	defer cancel()
	ticker := d.clock.NewTicker(wire.HeartbeatInterval)
	defer ticker.Stop()

	sw, err := wire.NewWriter(w)
	if err != nil {
		ctrllog.FromContext(r.Context()).Error(err, "open event stream", "run", run.Name)
		return
	}
	written := lastID
	write := func(e wire.LogEvent) (end bool) {
		data := e.Data
		if len(data) == 0 {
			data = json.RawMessage("{}")
		}
		if err := sw.Event(e.ID, e.Type, data); err != nil {
			return true
		}
		written = max(written, e.ID)
		if e.Type == wire.EventEnd {
			d.markFetchedLogged(r, run.Name)
			return true
		}
		return false
	}
	endFromStatus := func(run *v1alpha1.AgentRun) {
		data, err := json.Marshal(agentrun.EndData(run))
		if err != nil {
			ctrllog.FromContext(r.Context()).Error(err, "encode end event", "run", run.Name)
			return
		}
		write(wire.LogEvent{ID: written + 1, Event: wire.Event{Type: wire.EventEnd, Data: data}})
	}
	terminalNow := func() (*v1alpha1.AgentRun, bool) {
		cur := &v1alpha1.AgentRun{}
		if err := d.client.Get(r.Context(), client.ObjectKeyFromObject(run), cur); err != nil {
			return nil, false
		}
		return cur, cur.Status.Phase.Terminal()
	}

	for _, e := range backlog {
		if write(e) {
			return
		}
	}
	if run.Status.Phase.Terminal() {
		endFromStatus(run)
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-live:
			if !ok {
				if cur, terminal := terminalNow(); terminal {
					endFromStatus(cur)
				}
				return
			}
			if write(e) {
				return
			}
		case <-ticker.C():
			if err := sw.Heartbeat(); err != nil {
				return
			}
			if cur, terminal := terminalNow(); terminal {
				endFromStatus(cur)
				return
			}
		}
	}
}
