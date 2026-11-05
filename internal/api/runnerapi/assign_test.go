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

func TestRunnerRoutesForgetDropsPodStateInEitherOrder(t *testing.T) {
	for name, forgetFirst := range map[string]bool{"forget then detach": true, "detach then forget": false} {
		t.Run(name, func(t *testing.T) {
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
			forget := func() {
				h.Forget("r")
				rr.ForgetRun("r")
			}
			detach := func() {
				rr.detach(conn)
				conn.Detach()
			}
			if forgetFirst {
				forget()
				if st.token != "" {
					t.Fatalf("token %q kept after forget", st.token)
				}
				detach()
			} else {
				detach()
				forget()
			}
			if len(rr.pods) != 0 {
				t.Fatalf("runner pods %v; want empty", rr.pods)
			}
			if err := h.Send("p", "other", steer("after forget")); !errors.Is(err, hub.ErrNoStream) {
				t.Fatalf("send after forget err = %v, want ErrNoStream", err)
			}
			again, err := h.AttachRunner(pod, 0)
			if err != nil {
				t.Fatalf("attach after forget: %v", err)
			}
			if b := again.Backlog(); len(b) != 1 || string(b[0].Data) != string(steer("after forget").Data) {
				t.Fatalf("backlog after forget = %+v, want only the command sent after forget", b)
			}
		})
	}
}

func steer(msg string) wire.Command {
	data, _ := json.Marshal(wire.Steer{Message: msg})
	return wire.Command{Type: wire.CommandSteer, Data: data}
}
