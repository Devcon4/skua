package dev

import (
	"context"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/api/auth"
	"github.com/Devcon4/skua/internal/contract/wire"
)

func (d *routes) listRuns(w http.ResponseWriter, r *http.Request) {
	var list v1alpha1.AgentRunList
	if err := d.client.List(r.Context(), &list, client.InNamespace(d.cfg.RunNamespace),
		client.MatchingLabels{v1alpha1.LabelOwner: auth.OwnerFrom(r.Context())}); err != nil {
		serverError(w, r, "list runs", err)
		return
	}
	out := make([]wire.Run, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, agentrun.View(&list.Items[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *routes) getRun(w http.ResponseWriter, r *http.Request) {
	run, ok := d.ownedRun(w, r)
	if !ok {
		return
	}
	if run.Status.Phase.Terminal() {
		if err := d.markFetched(r.Context(), run); err != nil {
			serverError(w, r, "mark run fetched", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, agentrun.View(run))
}

func (d *routes) markFetched(ctx context.Context, run *v1alpha1.AgentRun) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if !run.Status.Phase.Terminal() || run.Annotations[v1alpha1.AnnotationFetchedAt] != "" {
			return nil
		}
		base := run.DeepCopy()
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations[v1alpha1.AnnotationFetchedAt] = d.clock.Now().UTC().Format(time.RFC3339)
		err := d.client.Patch(ctx, run, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if apierrors.IsConflict(err) {
			if gerr := d.client.Get(ctx, client.ObjectKeyFromObject(run), run); gerr != nil {
				return gerr
			}
		}
		return err
	})
}

func (d *routes) markFetchedLogged(r *http.Request, name string) {
	run := &v1alpha1.AgentRun{}
	err := d.client.Get(r.Context(), client.ObjectKey{Namespace: d.cfg.RunNamespace, Name: name}, run)
	if err == nil {
		err = d.markFetched(r.Context(), run)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		ctrllog.FromContext(r.Context()).Error(err, "mark run fetched", "run", name)
	}
}
