package dev

import (
	"encoding/json"
	"net/http"

	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/contract/wire"
)

type runEventStream struct {
	d       *routes
	r       *http.Request
	run     *v1alpha1.AgentRun
	sw      *wire.Writer
	written int64
}

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
	s := &runEventStream{d: d, r: r, run: run, sw: sw, written: lastID}

	for _, e := range backlog {
		if s.send(e) {
			return
		}
	}
	if run.Status.Phase.Terminal() {
		s.endFromStatus(run)
		return
	}
	s.follow(live, ticker)
}

func (s *runEventStream) follow(live <-chan wire.LogEvent, ticker clock.Ticker) {
	for {
		select {
		case <-s.r.Context().Done():
			return
		case e, ok := <-live:
			if !ok {
				s.endIfRunTerminal()
				return
			}
			if s.send(e) {
				return
			}
		case <-ticker.C():
			if err := s.sw.Heartbeat(); err != nil {
				return
			}
			if s.endIfRunTerminal() {
				return
			}
		}
	}
}

func (s *runEventStream) send(e wire.LogEvent) (streamEnded bool) {
	data := e.Data
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	if err := s.sw.Event(e.ID, e.Type, data); err != nil {
		return true
	}
	s.written = max(s.written, e.ID)
	if e.Type != wire.EventEnd {
		return false
	}
	s.d.markFetchedLogged(s.r, s.run.Name)
	return true
}

func (s *runEventStream) endFromStatus(run *v1alpha1.AgentRun) {
	data, err := json.Marshal(agentrun.EndData(run))
	if err != nil {
		ctrllog.FromContext(s.r.Context()).Error(err, "encode end event", "run", run.Name)
		return
	}
	s.send(wire.LogEvent{ID: s.written + 1, Event: wire.Event{Type: wire.EventEnd, Data: data}})
}

func (s *runEventStream) endIfRunTerminal() (ended bool) {
	cur := &v1alpha1.AgentRun{}
	if err := s.d.client.Get(s.r.Context(), client.ObjectKeyFromObject(s.run), cur); err != nil {
		return false
	}
	if !cur.Status.Phase.Terminal() {
		return false
	}
	s.endFromStatus(cur)
	return true
}
