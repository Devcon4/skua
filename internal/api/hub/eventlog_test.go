package hub

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func TestHubDedupsSeqsAndAcksHighest(t *testing.T) {
	h, _ := hubTestHub()
	if ack := hubAppend(t, h, "r", hubRunnerEvents(1, 2, 2)); ack != 2 {
		t.Fatalf("ack = %d, want 2", ack)
	}
	if ack := hubAppend(t, h, "r", hubRunnerEvents(4, 3, 1, 2)); ack != 4 {
		t.Fatalf("ack = %d, want 4", ack)
	}
	if ack := hubAppend(t, h, "r", hubRunnerEvents(3)); ack != 4 {
		t.Fatalf("ack = %d, want 4", ack)
	}
	backlog, _, cancel := h.Subscribe("r", 0)
	defer cancel()
	hubEqualIDs(t, h, hubIDs(backlog), 1, 2, 3, 4)
	var deltas []string
	for _, e := range backlog {
		var d wire.TextData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		deltas = append(deltas, d.Delta)
	}
	if got, want := deltas, []string{"b", "c", "e", "d"}; !slices.Equal(got, want) {
		t.Fatalf("deltas = %v, want %v", got, want)
	}
}

func TestHubSeqsAreDedupedPerRun(t *testing.T) {
	h, _ := hubTestHub()
	hubAppend(t, h, "a", hubRunnerEvents(1))
	if ack := hubAppend(t, h, "b", hubRunnerEvents(1)); ack != 1 {
		t.Fatalf("ack = %d, want 1", ack)
	}
	backlog, _, cancel := h.Subscribe("b", 0)
	defer cancel()
	hubEqualIDs(t, h, hubIDs(backlog), 1)
}

func TestHubRunnerEventsStopAtLogBudget(t *testing.T) {
	h, _ := hubTestHub()
	chunk, err := json.Marshal(wire.TextData{Delta: strings.Repeat("x", 1<<20)})
	if err != nil {
		t.Fatal(err)
	}
	var evs []wire.RunnerEvent
	for seq := int64(1); seq <= EventLogBudget/(1<<20)+1; seq++ {
		evs = append(evs, wire.RunnerEvent{Seq: seq, Type: wire.EventText, Data: chunk})
	}
	ack, err := h.AppendRunnerEvents("r", evs)
	if !errors.Is(err, ErrLogFull) {
		t.Fatalf("err = %v, want ErrLogFull", err)
	}
	stored := int64(EventLogBudget / (eventOverheadBytes + len(chunk) + len(wire.EventText)))
	if ack != stored {
		t.Fatalf("ack = %d, want %d", ack, stored)
	}
	if _, err := h.AppendRunnerEvents("r", evs[:1]); err != nil {
		t.Fatalf("re-post of a stored event: %v", err)
	}
	if _, err := h.AppendRunnerEvents("r", hubRunnerEvents(100)); !errors.Is(err, ErrLogFull) {
		t.Fatalf("small event after the budget: err = %v, want ErrLogFull", err)
	}
	if _, err := h.AppendRunnerEvents("other", evs[:1]); err != nil {
		t.Fatalf("another run: %v", err)
	}
	h.AppendEvent("r", wire.Event{Type: wire.EventEnd, Data: json.RawMessage(`{"phase":"Failed"}`)})
	backlog, _, cancel := h.Subscribe("r", 0)
	defer cancel()
	if n := int64(len(backlog)); n != stored+1 || backlog[n-1].Type != wire.EventEnd {
		t.Fatalf("log has %d events ending in %q, want %d ending in end", n, backlog[n-1].Type, stored+1)
	}
}

func TestHubEmptyRunnerEventsStillCostOverhead(t *testing.T) {
	h, _ := hubTestHub()
	perEvent := eventOverheadBytes + len(wire.EventText)
	want := int64(EventLogBudget / perEvent)
	evs := make([]wire.RunnerEvent, 0, want+1)
	for seq := int64(1); seq <= want+1; seq++ {
		evs = append(evs, wire.RunnerEvent{Seq: seq, Type: wire.EventText})
	}
	ack, err := h.AppendRunnerEvents("r", evs)
	if !errors.Is(err, ErrLogFull) {
		t.Fatalf("err = %v, want ErrLogFull", err)
	}
	if ack != want {
		t.Fatalf("ack = %d, want %d", ack, want)
	}
}

func TestHubSubscribeReturnsEventsAfterLastID(t *testing.T) {
	h, _ := hubTestHub()
	hubAppend(t, h, "r", hubRunnerEvents(1, 2, 3, 4, 5))
	backlog, _, cancel := h.Subscribe("r", h.base+3)
	defer cancel()
	hubEqualIDs(t, h, hubIDs(backlog), 4, 5)
}

func TestHubSubscribeBelowBaseReturnsWholeLog(t *testing.T) {
	h, _ := hubTestHub()
	hubAppend(t, h, "r", hubRunnerEvents(1, 2, 3, 4, 5))
	for _, lastID := range []int64{42, h.base - 1} {
		backlog, _, cancel := h.Subscribe("r", lastID)
		cancel()
		hubEqualIDs(t, h, hubIDs(backlog), 1, 2, 3, 4, 5)
	}
}

func TestHubSubscribeBeyondLogReturnsNothingNew(t *testing.T) {
	h, _ := hubTestHub()
	hubAppend(t, h, "r", hubRunnerEvents(1, 2))
	backlog, live, cancel := h.Subscribe("r", h.base+42)
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("backlog = %v, want empty", hubIDs(backlog))
	}
	hubAppend(t, h, "r", hubRunnerEvents(3))
	hubEqualIDs(t, h, []int64{(<-live).ID}, 3)
}

func TestHubSubscribeDeliversLiveEventsInLogOrder(t *testing.T) {
	h, _ := hubTestHub()
	hubAppend(t, h, "r", hubRunnerEvents(1, 2))
	backlog, live, cancel := h.Subscribe("r", h.base+1)
	defer cancel()
	hubEqualIDs(t, h, hubIDs(backlog), 2)
	hubAppend(t, h, "r", hubRunnerEvents(3))
	h.AppendEvent("r", wire.Event{Type: wire.EventStatus, Data: json.RawMessage(`{"phase":"Running"}`)})
	got := []wire.LogEvent{<-live, <-live}
	hubEqualIDs(t, h, hubIDs(got), 3, 4)
	if got[1].Type != wire.EventStatus {
		t.Fatalf("type = %q, want %q", got[1].Type, wire.EventStatus)
	}
}

func TestHubSubscribeBeforeAnyEventGetsLaterEvents(t *testing.T) {
	h, _ := hubTestHub()
	backlog, live, cancel := h.Subscribe("r", 0)
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("backlog = %v, want empty", backlog)
	}
	h.AppendEvent("r", wire.Event{Type: wire.EventStatus, Data: json.RawMessage(`{"phase":"Running"}`)})
	hubEqualIDs(t, h, []int64{(<-live).ID}, 1)
}

func TestHubCancelClosesLiveChannel(t *testing.T) {
	h, _ := hubTestHub()
	_, live, cancel := h.Subscribe("r", 0)
	cancel()
	cancel()
	if _, ok := <-live; ok {
		t.Fatal("live channel open after cancel")
	}
	h.AppendEvent("r", wire.Event{Type: wire.EventStatus, Data: json.RawMessage(`{}`)})
}

func TestHubBlockedSubscriberDoesNotBlockAppendEvent(t *testing.T) {
	h, _ := hubTestHub()
	_, live, cancel := h.Subscribe("r", 0)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range subscriberBuffer * 4 {
			h.AppendEvent("r", wire.Event{Type: wire.EventText, Data: hubText("x")})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AppendEvent blocked on a slow subscriber")
	}
	n := 0
	for range live {
		n++
	}
	if n != subscriberBuffer {
		t.Fatalf("received %d events before close, want %d", n, subscriberBuffer)
	}
}
