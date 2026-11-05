package runnerapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/wire"
)

var eventTypes = []string{wire.EventText, wire.EventThinking, wire.EventToolStart, wire.EventToolUpdate, wire.EventToolEnd}

func (rr *Routes) events(w http.ResponseWriter, r *http.Request) {
	pod, ok := rr.authenticate(w, r)
	if !ok {
		return
	}
	var req wire.RunnerEventsRequest
	if err := decode(w, r, wire.MaxEventsBodyBytes, &req); err != nil {
		refuse(w, decodeRefusal(err))
		return
	}
	run, ok := rr.callerRun(r.Context(), w, pod, req.Run)
	if !ok {
		return
	}
	if run.Status.Phase.Terminal() {
		refuse(w, &refusal{http.StatusConflict, wire.RefusalRunFinished, fmt.Sprintf("run %s is %s", run.Name, run.Status.Phase)})
		return
	}
	for _, ev := range req.Events {
		if !slices.Contains(eventTypes, ev.Type) {
			refuse(w, &refusal{http.StatusBadRequest, wire.RefusalBadRequest, fmt.Sprintf("event type %q is not one of %v", ev.Type, eventTypes)})
			return
		}
	}
	ack, err := rr.deps.Hub.AppendRunnerEvents(req.Run, req.Events)
	if err != nil {
		refuse(w, &refusal{http.StatusRequestEntityTooLarge, wire.RefusalEventLogFull, fmt.Sprintf("run %s reached its %d-byte event budget", req.Run, hub.EventLogBudget)})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(wire.Ack{Ack: ack})
}
