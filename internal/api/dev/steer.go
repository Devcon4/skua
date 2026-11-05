package dev

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func (d *routes) steer(w http.ResponseWriter, r *http.Request) {
	run, ok := d.ownedRun(w, r)
	if !ok {
		return
	}
	var body wire.Steer
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Message == "" {
		http.Error(w, "message is empty", http.StatusBadRequest)
		return
	}
	if run.Status.Phase.Terminal() {
		http.Error(w, fmt.Sprintf("run %q is %s", run.Name, run.Status.Phase), http.StatusConflict)
		return
	}
	if run.Status.PodName == "" {
		http.Error(w, fmt.Sprintf("run %q has no pod yet", run.Name), http.StatusConflict)
		return
	}
	data, err := json.Marshal(body)
	if err != nil {
		serverError(w, r, "encode steer", err)
		return
	}
	if err := d.hub.Send(run.Status.PodName, run.Name, wire.Command{Type: wire.CommandSteer, Data: data}); err != nil && !errors.Is(err, hub.ErrNoStream) {
		serverError(w, r, "send steer", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
