package dev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// AbortGrace is how long an abort sent to an attached runner waits for its result before the run is cancelled.
const AbortGrace = 30 * time.Second

func (d *routes) abort(w http.ResponseWriter, r *http.Request) {
	run, ok := d.ownedRun(w, r)
	if !ok {
		return
	}
	if run.Status.Phase.Terminal() {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if run.Status.PodName != "" {
		err := d.hub.Send(run.Status.PodName, run.Name, wire.Command{Type: wire.CommandAbort, Data: json.RawMessage("{}")})
		if err == nil {
			timer := d.clock.NewTimer(AbortGrace)
			go d.cancelAfter(context.WithoutCancel(r.Context()), timer, client.ObjectKeyFromObject(run))
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if !errors.Is(err, hub.ErrNoStream) {
			serverError(w, r, "send abort", err)
			return
		}
	}
	if err := d.cancel(r.Context(), run, "aborted by its owner with no runner attached"); err != nil {
		serverError(w, r, "cancel run", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (d *routes) cancelAfter(ctx context.Context, timer clock.Timer, key client.ObjectKey) {
	<-timer.C()
	run := &v1alpha1.AgentRun{}
	err := d.client.Get(ctx, key, run)
	if err == nil {
		err = d.cancel(ctx, run, fmt.Sprintf("aborted by its owner; no result within %s", AbortGrace))
	}
	if client.IgnoreNotFound(err) != nil {
		ctrllog.FromContext(ctx).Error(err, "cancel run after abort grace", "run", key.Name)
	}
}

func (d *routes) cancel(ctx context.Context, run *v1alpha1.AgentRun, msg string) error {
	return agentrun.Finish(ctx, d.client, d.client, d.clock, run, func(st *v1alpha1.AgentRunStatus) {
		st.Phase, st.Reason, st.Message = v1alpha1.PhaseFailed, v1alpha1.ReasonCancelled, msg
	})
}
