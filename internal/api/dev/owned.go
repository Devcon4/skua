package dev

import (
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/auth"
)

func (d *routes) ownedRun(w http.ResponseWriter, r *http.Request) (*v1alpha1.AgentRun, bool) {
	name := r.PathValue("name")
	notFound := func() {
		http.Error(w, fmt.Sprintf("run %q not found", name), http.StatusNotFound)
	}
	if len(validation.IsDNS1123Subdomain(name)) > 0 {
		notFound()
		return nil, false
	}
	run := &v1alpha1.AgentRun{}
	if err := d.client.Get(r.Context(), client.ObjectKey{Namespace: d.cfg.RunNamespace, Name: name}, run); err != nil {
		if apierrors.IsNotFound(err) {
			notFound()
			return nil, false
		}
		serverError(w, r, "get run", err)
		return nil, false
	}
	if run.Labels[v1alpha1.LabelOwner] != auth.OwnerFrom(r.Context()) {
		notFound()
		return nil, false
	}
	return run, true
}
