// Package hub is skua-api's in-memory state for one process: the per-run event logs with their subscribers,
// and the per-pod runner command streams.
package hub

import (
	"errors"
	"sync"
	"time"

	"k8s.io/utils/clock"
)

var (
	// ErrStreamActive is returned by AttachRunner while the pod already has an attached stream.
	ErrStreamActive = errors.New("api: runner stream already active")
	// ErrNoStream is returned by Send when the pod has no attached stream; the command is kept for replay.
	ErrNoStream = errors.New("api: no runner stream attached")
	// ErrLogFull is returned by AppendRunnerEvents once the run's runner events reach EventLogBudget bytes.
	ErrLogFull = errors.New("api: run event log full")
)

// EventLogBudget is the number of bytes of runner events one run's log holds.
const EventLogBudget = 8 << 20

const (
	forgetAfter         = 5 * time.Minute
	subscriberBuffer    = 256
	runnerCommandBuffer = 64
	eventOverheadBytes  = 64
)

// Hub holds skua-api's in-memory state for one process: the per-run event logs with their subscribers,
// and the per-pod runner command streams. Event and command IDs start above the process's base, its start time
// in Unix microseconds, so they keep increasing across skua-api restarts.
type Hub struct {
	clock clock.PassiveClock
	base  int64

	mu          sync.Mutex
	runs        map[string]*runLog
	forgotten   map[string]time.Time
	pods        map[string]*podState
	lastCommand int64
}

// Pod names a runner's pod by name and UID.
type Pod struct {
	Name string
	UID  string
}

func New(clock clock.PassiveClock) *Hub {
	base := clock.Now().UnixMicro()
	return &Hub{
		clock:       clock,
		base:        base,
		runs:        map[string]*runLog{},
		forgotten:   map[string]time.Time{},
		pods:        map[string]*podState{},
		lastCommand: base,
	}
}

// Forget drops the run's log forgetAfter from now, so late reconnects still get the end event. It drops the
// commands kept for the pods serving run at once, and their pod state as soon as their stream is gone.
func (h *Hub) Forget(run string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep()
	if _, ok := h.forgotten[run]; !ok {
		h.forgotten[run] = h.clock.Now()
	}
	for name, st := range h.pods {
		if st.run != run {
			continue
		}
		if st.conn == nil {
			delete(h.pods, name)
			continue
		}
		clear(st.commands)
		st.commands = nil
		st.done = true
	}
}

func (h *Hub) sweep() {
	now := h.clock.Now()
	for run, at := range h.forgotten {
		if now.Sub(at) < forgetAfter {
			continue
		}
		delete(h.forgotten, run)
		if l, ok := h.runs[run]; ok {
			for sub := range l.subs {
				l.drop(sub)
			}
			delete(h.runs, run)
		}
	}
}
