package runnerapi

import (
	"errors"
	"net/http"
	"time"

	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func (rr *Routes) stream(w http.ResponseWriter, r *http.Request) {
	pod, ok := rr.authenticate(w, r)
	if !ok {
		return
	}
	lastID, _ := wire.LastEventID(r)
	ref := hub.Pod{Name: pod.Name, UID: string(pod.UID)}
	conn, err := rr.deps.Hub.AttachRunner(ref, lastID)
	if errors.Is(err, hub.ErrStreamActive) {
		refuse(w, &refusal{http.StatusConflict, wire.RefusalStreamActive, "pod " + pod.Name + " already has an open stream"})
		return
	}
	if err != nil {
		refuse(w, &refusal{http.StatusInternalServerError, wire.RefusalInternalError, err.Error()})
		return
	}
	defer conn.Detach()
	sw, err := wire.NewWriter(w)
	if err != nil {
		return
	}
	for _, c := range conn.Backlog() {
		if sw.Event(c.ID, c.Type, c.Data) != nil {
			return
		}
	}
	rr.attach(conn)
	defer rr.detach(conn)
	rr.Assign(r.Context(), pod.Name)

	tick := newTicker(rr.deps.Clock, wire.HeartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case c, open := <-conn.Commands():
			if !open || sw.Event(c.ID, c.Type, c.Data) != nil {
				return
			}
		case <-tick.C():
			if sw.Heartbeat() != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (rr *Routes) attach(conn *hub.RunnerConn) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	pod := conn.Pod()
	st := rr.pods[pod.Name]
	if st == nil || st.uid != pod.UID {
		st = &podState{uid: pod.UID}
		rr.pods[pod.Name] = st
	}
	st.conn = conn
}

func (rr *Routes) detach(conn *hub.RunnerConn) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	pod := conn.Pod()
	st := rr.pods[pod.Name]
	if st == nil || st.conn != conn {
		return
	}
	st.conn = nil
	if st.run == "" || st.done {
		delete(rr.pods, pod.Name)
	}
}

func newTicker(c clock.PassiveClock, d time.Duration) clock.Ticker {
	if tc, ok := c.(clock.WithTicker); ok {
		return tc.NewTicker(d)
	}
	return clock.RealClock{}.NewTicker(d)
}
