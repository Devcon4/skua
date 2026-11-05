package runnerapi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/wire"
)

type assignedStream struct {
	h    *hub.Hub
	rr   *Routes
	pod  hub.Pod
	conn *hub.RunnerConn
	st   *podState
}

func newAssignedStream(t *testing.T) *assignedStream {
	h := hub.New(clocktesting.NewFakeClock(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)))
	rr := &Routes{deps: Deps{Hub: h}, pods: map[string]*podState{}}
	pod := hub.Pod{Name: "p", UID: "u1"}
	conn, err := h.AttachRunner(pod, 0)
	if err != nil {
		t.Fatal(err)
	}
	rr.attach(conn)
	rr.mu.Lock()
	st := rr.pods["p"]
	st.assigned, st.run, st.token = conn, "r", "ghs-secret"
	rr.mu.Unlock()
	if err := h.Send("p", "r", steer("hi")); err != nil {
		t.Fatal(err)
	}
	return &assignedStream{h: h, rr: rr, pod: pod, conn: conn, st: st}
}

func (s *assignedStream) forget() {
	s.h.Forget("r")
	s.rr.ForgetRun("r")
}

func (s *assignedStream) detach() {
	s.rr.detach(s.conn)
	s.conn.Detach()
}

func (s *assignedStream) requireForgotten(t *testing.T) {
	if len(s.rr.pods) != 0 {
		t.Fatalf("runner pods %v; want empty", s.rr.pods)
	}
	if err := s.h.Send("p", "other", steer("after forget")); !errors.Is(err, hub.ErrNoStream) {
		t.Fatalf("send after forget err = %v, want ErrNoStream", err)
	}
	again, err := s.h.AttachRunner(s.pod, 0)
	if err != nil {
		t.Fatalf("attach after forget: %v", err)
	}
	if b := again.Backlog(); len(b) != 1 || string(b[0].Data) != string(steer("after forget").Data) {
		t.Fatalf("backlog after forget = %+v, want only the command sent after forget", b)
	}
}

func TestRunnerRoutesForgetDropsPodStateInEitherOrder(t *testing.T) {
	teardowns := map[string]func(t *testing.T, s *assignedStream){
		"forget then detach": func(t *testing.T, s *assignedStream) {
			s.forget()
			if s.st.token != "" {
				t.Fatalf("token %q kept after forget", s.st.token)
			}
			s.detach()
		},
		"detach then forget": func(_ *testing.T, s *assignedStream) {
			s.detach()
			s.forget()
		},
	}
	for name, teardown := range teardowns {
		t.Run(name, func(t *testing.T) {
			s := newAssignedStream(t)
			teardown(t, s)
			s.requireForgotten(t)
		})
	}
}

func steer(msg string) wire.Command {
	data, _ := json.Marshal(wire.Steer{Message: msg})
	return wire.Command{Type: wire.CommandSteer, Data: data}
}
