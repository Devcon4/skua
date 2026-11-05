package dev

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/api/auth"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

const (
	maxClientKey   = 256
	maxBaseRef     = 255
	maxProfileName = 50
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (d *routes) createRun(w http.ResponseWriter, r *http.Request) {
	req, err := decodeCreate(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	owner := auth.OwnerFrom(r.Context())
	name := RunName(req.Profile, owner, req.ClientKey)

	existing := &v1alpha1.AgentRun{}
	err = d.client.Get(r.Context(), client.ObjectKey{Namespace: d.cfg.RunNamespace, Name: name}, existing)
	switch {
	case err == nil:
		d.respondExisting(w, existing, owner, req.ClientKey)
		return
	case !apierrors.IsNotFound(err):
		serverError(w, r, "get run", err)
		return
	}

	profile := &v1alpha1.AgentProfile{}
	if err := d.client.Get(r.Context(), client.ObjectKey{Name: req.Profile}, profile); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("profile %q not found", req.Profile), http.StatusNotFound)
			return
		}
		serverError(w, r, "get profile", err)
		return
	}
	if !meta.IsStatusConditionTrue(profile.Status.Conditions, v1alpha1.ConditionReady) {
		http.Error(w, fmt.Sprintf("profile %q is not Ready", req.Profile), http.StatusConflict)
		return
	}

	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: d.cfg.RunNamespace,
			Labels:    map[string]string{v1alpha1.LabelOwner: owner, sandbox.LabelProfile: req.Profile},
		},
		Spec: v1alpha1.AgentRunSpec{
			Profile:   req.Profile,
			ClientKey: req.ClientKey,
			Task:      v1alpha1.Task{Prompt: req.Task.Prompt},
		},
	}
	if req.Workspace != nil {
		run.Spec.Workspace = &v1alpha1.RunWorkspace{Repo: req.Workspace.Repo, BaseRef: req.Workspace.BaseRef}
	}
	if len(req.Task.Schema) > 0 {
		run.Spec.Task.Schema = &runtime.RawExtension{Raw: req.Task.Schema}
	}
	err = d.client.Create(r.Context(), run)
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, agentrun.View(run))
	case apierrors.IsAlreadyExists(err):
		if err := d.client.Get(r.Context(), client.ObjectKeyFromObject(run), existing); err != nil {
			serverError(w, r, "get existing run", err)
			return
		}
		d.respondExisting(w, existing, owner, req.ClientKey)
	case apierrors.IsInvalid(err):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		serverError(w, r, "create run", err)
	}
}

func (d *routes) respondExisting(w http.ResponseWriter, run *v1alpha1.AgentRun, owner, clientKey string) {
	if run.Labels[v1alpha1.LabelOwner] != owner || run.Spec.ClientKey != clientKey {
		http.Error(w, fmt.Sprintf("run name %q is taken", run.Name), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, agentrun.View(run))
}

func decodeCreate(w http.ResponseWriter, r *http.Request) (*wire.CreateRunRequest, error) {
	var req wire.CreateRunRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, err
	}
	switch {
	case req.Profile == "":
		return nil, errors.New("profile is empty")
	case len(req.Profile) > maxProfileName || len(validation.IsDNS1123Subdomain(req.Profile)) > 0:
		return nil, fmt.Errorf("profile %q is not a valid profile name", req.Profile)
	case req.ClientKey == "":
		return nil, errors.New("clientKey is empty")
	case len(req.ClientKey) > maxClientKey:
		return nil, fmt.Errorf("clientKey is longer than %d bytes", maxClientKey)
	case req.Task.Prompt == "":
		return nil, errors.New("task.prompt is empty")
	}
	if ws := req.Workspace; ws != nil {
		if !repoPattern.MatchString(ws.Repo) {
			return nil, fmt.Errorf("workspace.repo %q is not owner/name", ws.Repo)
		}
		if ws.BaseRef == "" || len(ws.BaseRef) > maxBaseRef {
			return nil, fmt.Errorf("workspace.baseRef must be 1 to %d bytes", maxBaseRef)
		}
	}
	if string(req.Task.Schema) == "null" {
		req.Task.Schema = nil
	}
	return &req, nil
}

// RunName is the name of the run that owner creates from profile with clientKey.
func RunName(profile, owner, clientKey string) string {
	sum := sha256.Sum256([]byte(owner + "\n" + clientKey))
	return profile + "-" + hex.EncodeToString(sum[:])[:10]
}
