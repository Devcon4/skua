package hub

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func TestHubDetachOfUnassignedPodDropsItsState(t *testing.T) {
	h, _ := hubTestHub()
	conn, err := h.AttachRunner(Pod{Name: "p", UID: "u1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn.Detach()
	if len(h.pods) != 0 {
		t.Fatalf("hub pods = %v, want empty", h.pods)
	}
}

func TestHubSecondAttachRunnerIsStreamActiveUntilDetach(t *testing.T) {
	h, _ := hubTestHub()
	pod := Pod{Name: "p", UID: "u1"}
	conn, err := h.AttachRunner(pod, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.AttachRunner(pod, 0); !errors.Is(err, ErrStreamActive) {
		t.Fatalf("second attach err = %v, want ErrStreamActive", err)
	}
	conn.Detach()
	conn.Detach()
	if _, ok := <-conn.Commands(); ok {
		t.Fatal("commands channel open after detach")
	}
	if _, err := h.AttachRunner(pod, 0); err != nil {
		t.Fatalf("attach after detach: %v", err)
	}
}

func TestHubCommandIDsStartAboveBase(t *testing.T) {
	h, _ := hubTestHub()
	conn, err := h.AttachRunner(Pod{Name: "p", UID: "u1"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.Backlog()) != 0 {
		t.Fatalf("backlog = %v, want empty", conn.Backlog())
	}
	if err := h.Send("p", "r", hubSteer("hi")); err != nil {
		t.Fatal(err)
	}
	if c := hubRecv(t, conn.Commands()); c.ID != h.base+1 || c.Type != wire.CommandSteer {
		t.Fatalf("command = %+v, want id base+1 steer", c)
	}
}

func TestHubSendToDetachedPodIsReplayedOnNextAttach(t *testing.T) {
	h, _ := hubTestHub()
	pod := Pod{Name: "p", UID: "u1"}
	conn, err := h.AttachRunner(pod, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Send("p", "r", wire.Command{Type: wire.CommandAssign, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	first := hubRecv(t, conn.Commands())
	conn.Detach()

	if err := h.Send("p", "r", hubSteer("hi")); !errors.Is(err, ErrNoStream) {
		t.Fatalf("send to detached pod err = %v, want ErrNoStream", err)
	}
	conn, err = h.AttachRunner(pod, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	backlog := conn.Backlog()
	if len(backlog) != 1 || backlog[0].ID != first.ID+1 || backlog[0].Type != wire.CommandSteer {
		t.Fatalf("backlog = %+v, want steer with id %d", backlog, first.ID+1)
	}
	if err := h.Send("p", "r", wire.Command{Type: wire.CommandAbort, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if c := hubRecv(t, conn.Commands()); c.ID != first.ID+2 {
		t.Fatalf("id = %d, want %d", c.ID, first.ID+2)
	}
}

func TestHubNewPodUIDStartsAfresh(t *testing.T) {
	h, _ := hubTestHub()
	conn, err := h.AttachRunner(Pod{Name: "p", UID: "u1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Send("p", "r", wire.Command{Type: wire.CommandAssign, Data: json.RawMessage(`{}`)})
	conn.Detach()
	_ = h.Send("p", "r", hubSteer(""))

	conn, err = h.AttachRunner(Pod{Name: "p", UID: "u2"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.Backlog()) != 0 {
		t.Fatalf("backlog = %+v, want empty", conn.Backlog())
	}
}

func TestHubSlowRunnerStreamIsDetached(t *testing.T) {
	h, _ := hubTestHub()
	pod := Pod{Name: "p", UID: "u1"}
	if _, err := h.AttachRunner(pod, 0); err != nil {
		t.Fatal(err)
	}
	var err error
	for range runnerCommandBuffer + 1 {
		err = h.Send("p", "r", hubSteer(""))
	}
	if !errors.Is(err, ErrNoStream) {
		t.Fatalf("err = %v, want ErrNoStream", err)
	}
	conn, err := h.AttachRunner(pod, h.base+runnerCommandBuffer)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if b := conn.Backlog(); len(b) != 1 || b[0].ID != h.base+runnerCommandBuffer+1 {
		t.Fatalf("backlog = %+v, want the undelivered command", b)
	}
}
