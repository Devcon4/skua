package hub

import (
	"slices"

	"github.com/Devcon4/skua/internal/contract/wire"
)

type podState struct {
	ref      Pod
	commands []wire.Command
	conn     *RunnerConn
	run      string
	done     bool
}

// RunnerConn is one attached runner command stream.
type RunnerConn struct {
	hub     *Hub
	pod     Pod
	backlog []wire.Command
	ch      chan wire.Command
	closed  bool
}

// AttachRunner attaches the pod's command stream and queues for replay the stored commands with an ID above
// lastID, or all of them when lastID is below the hub's base and so comes from an earlier skua-api process.
// A pod with another UID than the stored one starts afresh.
func (h *Hub) AttachRunner(pod Pod, lastID int64) (*RunnerConn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep()
	st := h.pods[pod.Name]
	if st != nil && st.conn != nil {
		return nil, ErrStreamActive
	}
	if st == nil || (st.ref.UID != "" && st.ref.UID != pod.UID) {
		st = &podState{}
		h.pods[pod.Name] = st
	}
	st.ref = pod
	if lastID >= h.base {
		st.commands = slices.DeleteFunc(st.commands, func(c wire.Command) bool { return c.ID <= lastID })
	}
	conn := &RunnerConn{
		hub:     h,
		pod:     pod,
		backlog: append([]wire.Command(nil), st.commands...),
		ch:      make(chan wire.Command, runnerCommandBuffer),
	}
	st.conn = conn
	return conn, nil
}

// Send records that pod serves run, gives c the next command ID, stores it for replay on the next AttachRunner
// and delivers it to the attached stream. It returns ErrNoStream when the pod has no attached stream.
func (h *Hub) Send(pod, run string, c wire.Command) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep()
	st := h.pods[pod]
	if st == nil {
		st = &podState{ref: Pod{Name: pod}}
		h.pods[pod] = st
	}
	st.run = run
	h.lastCommand++
	c.ID = h.lastCommand
	st.commands = append(st.commands, c)
	if st.conn == nil {
		return ErrNoStream
	}
	select {
	case st.conn.ch <- c:
		return nil
	default:
		st.conn.closeLocked()
		return ErrNoStream
	}
}

// Pod is the pod this stream belongs to.
func (c *RunnerConn) Pod() Pod {
	return c.pod
}

// Backlog is the stored commands replayed on attach, in ID order; they precede everything on Commands.
func (c *RunnerConn) Backlog() []wire.Command {
	return c.backlog
}

// Commands delivers the commands sent after attach. It is closed by Detach, or when the stream falls
// runnerCommandBuffer commands behind, which also detaches it.
func (c *RunnerConn) Commands() <-chan wire.Command {
	return c.ch
}

// Detach releases the pod's stream so the pod can attach again. It is safe to call more than once.
func (c *RunnerConn) Detach() {
	c.hub.mu.Lock()
	defer c.hub.mu.Unlock()
	c.closeLocked()
}

func (c *RunnerConn) closeLocked() {
	if c.closed {
		return
	}
	c.closed = true
	close(c.ch)
	if st := c.hub.pods[c.pod.Name]; st != nil && st.conn == c {
		st.conn = nil
		if st.done || st.run == "" {
			delete(c.hub.pods, c.pod.Name)
		}
	}
}
