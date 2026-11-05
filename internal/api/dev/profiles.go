package dev

import (
	"net/http"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// +kubebuilder:rbac:groups=skua.dev,resources=agentprofiles,verbs=get;list;watch

func (d *routes) listProfiles(w http.ResponseWriter, r *http.Request) {
	var list v1alpha1.AgentProfileList
	if err := d.client.List(r.Context(), &list); err != nil {
		serverError(w, r, "list profiles", err)
		return
	}
	out := make([]wire.Profile, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		out = append(out, wire.Profile{
			Name:        p.Name,
			Description: p.Spec.Description,
			Models:      append([]string{}, p.Spec.Models...),
			Ready:       meta.IsStatusConditionTrue(p.Status.Conditions, v1alpha1.ConditionReady),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
