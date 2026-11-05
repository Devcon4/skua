package hub

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func hubTestHub() (*Hub, *clocktesting.FakeClock) {
	clk := clocktesting.NewFakeClock(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	return New(clk), clk
}

func hubText(delta string) json.RawMessage {
	b, _ := json.Marshal(wire.TextData{Delta: delta})
	return b
}

func hubRunnerEvents(seqs ...int64) []wire.RunnerEvent {
	evs := make([]wire.RunnerEvent, 0, len(seqs))
	for _, s := range seqs {
		evs = append(evs, wire.RunnerEvent{Seq: s, Type: wire.EventText, Data: hubText(string(rune('a' + s)))})
	}
	return evs
}

func hubAppend(t *testing.T, h *Hub, run string, evs []wire.RunnerEvent) int64 {
	t.Helper()
	ack, err := h.AppendRunnerEvents(run, evs)
	if err != nil {
		t.Fatalf("AppendRunnerEvents: %v", err)
	}
	return ack
}

func hubIDs(evs []wire.LogEvent) []int64 {
	ids := make([]int64, 0, len(evs))
	for _, e := range evs {
		ids = append(ids, e.ID)
	}
	return ids
}

func hubEqualIDs(t *testing.T, h *Hub, got []int64, positions ...int64) {
	t.Helper()
	want := make([]int64, 0, len(positions))
	for _, p := range positions {
		want = append(want, h.base+p)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func hubRecv(t *testing.T, ch <-chan wire.Command) wire.Command {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("command channel closed")
		}
		return c
	case <-time.After(time.Second):
		t.Fatal("no command delivered")
	}
	return wire.Command{}
}

func hubSteer(msg string) wire.Command {
	data, _ := json.Marshal(wire.Steer{Message: msg})
	return wire.Command{Type: wire.CommandSteer, Data: data}
}

func TestHubBaseIsStartTimeInMicroseconds(t *testing.T) {
	h, clk := hubTestHub()
	if h.base != clk.Now().UnixMicro() {
		t.Fatalf("base = %d, want %d", h.base, clk.Now().UnixMicro())
	}
	if h.base >= 1<<53 {
		t.Fatalf("base %d is not exact in a JavaScript number", h.base)
	}
}

func TestHubForgetDropsLogAfterFiveMinutes(t *testing.T) {
	h, clk := hubTestHub()
	hubAppend(t, h, "r", hubRunnerEvents(1))
	h.AppendEvent("r", wire.Event{Type: wire.EventEnd, Data: json.RawMessage(`{"phase":"Succeeded","reason":"Completed"}`)})
	_, live, cancel := h.Subscribe("r", h.base+2)
	defer cancel()
	h.Forget("r")

	clk.Step(forgetAfter - time.Second)
	backlog, _, cancelLate := h.Subscribe("r", 0)
	cancelLate()
	hubEqualIDs(t, h, hubIDs(backlog), 1, 2)
	if backlog[1].Type != wire.EventEnd {
		t.Fatalf("type = %q, want %q", backlog[1].Type, wire.EventEnd)
	}

	clk.Step(time.Second)
	backlog, _, cancelGone := h.Subscribe("r", 0)
	cancelGone()
	if len(backlog) != 0 {
		t.Fatalf("backlog after forget = %v, want empty", hubIDs(backlog))
	}
	if _, ok := <-live; ok {
		t.Fatal("live channel open after forget")
	}
}

func TestHubForgetDropsStateOfDetachedPods(t *testing.T) {
	h, _ := hubTestHub()
	_ = h.Send("p", "r", hubSteer("hi"))
	_ = h.Send("q", "other", hubSteer("keep"))
	h.Forget("r")
	if _, ok := h.pods["p"]; ok {
		t.Fatal("pod state of a forgotten run kept")
	}
	if st := h.pods["q"]; st == nil || len(st.commands) != 1 {
		t.Fatalf("pod state of another run = %+v, want its command kept", st)
	}
}

func TestHubForgetDropsStateOfAttachedPodsOnDetach(t *testing.T) {
	h, _ := hubTestHub()
	conn, err := h.AttachRunner(Pod{Name: "p", UID: "u1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Send("p", "r", hubSteer("secret")); err != nil {
		t.Fatal(err)
	}
	hubRecv(t, conn.Commands())
	h.Forget("r")
	if st := h.pods["p"]; st == nil || len(st.commands) != 0 {
		t.Fatalf("pod state after forget = %+v, want attached with no commands", st)
	}
	conn.Detach()
	if _, ok := h.pods["p"]; ok {
		t.Fatal("pod state of a forgotten run kept after detach")
	}
}

func TestHubNewProcessReplaysEverythingToClientsOfAnEarlierOne(t *testing.T) {
	old, clk := hubTestHub()
	pod := Pod{Name: "p", UID: "u1"}
	conn, err := old.AttachRunner(pod, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := old.Send("p", "r", hubSteer("old "+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	var lastCommand int64
	for range 3 {
		lastCommand = hubRecv(t, conn.Commands()).ID
	}
	hubAppend(t, old, "r", hubRunnerEvents(1, 2, 3))
	oldLog, _, cancel := old.Subscribe("r", 0)
	cancel()
	lastEvent := oldLog[len(oldLog)-1].ID

	clk.Step(time.Second)
	h := New(clk)
	if err := h.Send("p", "r", hubSteer("queued before attach")); !errors.Is(err, ErrNoStream) {
		t.Fatalf("send before attach err = %v, want ErrNoStream", err)
	}
	conn, err = h.AttachRunner(pod, lastCommand)
	if err != nil {
		t.Fatal(err)
	}
	backlog := conn.Backlog()
	if len(backlog) != 1 || backlog[0].Type != wire.CommandSteer || !strings.Contains(string(backlog[0].Data), "queued before attach") {
		t.Fatalf("backlog = %+v, want the steer queued before attach", backlog)
	}
	if backlog[0].ID <= lastCommand {
		t.Fatalf("command id %d is not above the earlier process's %d", backlog[0].ID, lastCommand)
	}

	hubAppend(t, h, "r", hubRunnerEvents(4, 5))
	events, _, cancel := h.Subscribe("r", lastEvent)
	defer cancel()
	hubEqualIDs(t, h, hubIDs(events), 1, 2)
	if events[0].ID <= lastEvent {
		t.Fatalf("event id %d is not above the earlier process's %d", events[0].ID, lastEvent)
	}
}
