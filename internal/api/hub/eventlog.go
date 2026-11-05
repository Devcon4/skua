package hub

import (
	"github.com/Devcon4/skua/internal/contract/wire"
)

type runLog struct {
	base   int64
	events []wire.LogEvent
	bytes  int
	full   bool
	seqs   map[int64]struct{}
	ack    int64
	subs   map[*subscriber]struct{}
}

type subscriber struct {
	ch     chan wire.LogEvent
	closed bool
}

// AppendRunnerEvents stores each event whose seq the run has not stored yet, in the given order, and returns the
// highest seq stored for the run. Each event costs eventOverheadBytes plus its type and data. At the first new
// event that would take the run past EventLogBudget it stops with ErrLogFull, and refuses every new event afterwards.
func (h *Hub) AppendRunnerEvents(run string, evs []wire.RunnerEvent) (ack int64, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep()
	l := h.log(run)
	for _, ev := range evs {
		if _, dup := l.seqs[ev.Seq]; dup {
			continue
		}
		size := eventOverheadBytes + len(ev.Type) + len(ev.Data)
		if l.full || l.bytes+size > EventLogBudget {
			l.full = true
			return l.ack, ErrLogFull
		}
		l.bytes += size
		l.seqs[ev.Seq] = struct{}{}
		l.ack = max(l.ack, ev.Seq)
		l.append(wire.Event{Type: ev.Type, Data: ev.Data})
	}
	return l.ack, nil
}

// AppendEvent appends an event made by skua-api itself, such as status and end, to the run's log.
func (h *Hub) AppendEvent(run string, e wire.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep()
	h.log(run).append(e)
}

// Subscribe returns the run's logged events with an ID above lastID, or the whole log when lastID is below the
// hub's base and so comes from an earlier skua-api process, and a channel of the events appended afterwards.
// The channel is closed by cancel, by Forget dropping the log, or when the subscriber falls subscriberBuffer
// events behind.
func (h *Hub) Subscribe(run string, lastID int64) (backlog []wire.LogEvent, live <-chan wire.LogEvent, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep()
	l := h.log(run)
	start := 0
	if lastID >= l.base {
		start = int(min(lastID-l.base, int64(len(l.events))))
	}
	backlog = append([]wire.LogEvent(nil), l.events[start:]...)
	sub := &subscriber{ch: make(chan wire.LogEvent, subscriberBuffer)}
	l.subs[sub] = struct{}{}
	cancel = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		l.drop(sub)
		if cur, ok := h.runs[run]; ok && cur == l && len(l.events) == 0 && len(l.subs) == 0 {
			delete(h.runs, run)
		}
	}
	return backlog, sub.ch, cancel
}

func (h *Hub) log(run string) *runLog {
	l, ok := h.runs[run]
	if !ok {
		l = &runLog{base: h.base, seqs: map[int64]struct{}{}, subs: map[*subscriber]struct{}{}}
		h.runs[run] = l
	}
	return l
}

func (l *runLog) append(e wire.Event) {
	le := wire.LogEvent{ID: l.base + int64(len(l.events)) + 1, Event: e}
	l.events = append(l.events, le)
	for sub := range l.subs {
		select {
		case sub.ch <- le:
		default:
			l.drop(sub)
		}
	}
}

func (l *runLog) drop(sub *subscriber) {
	delete(l.subs, sub)
	if !sub.closed {
		sub.closed = true
		close(sub.ch)
	}
}
