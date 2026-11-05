package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

type pullRef struct {
	Ref   string `json:"ref"`
	Label string `json:"label,omitempty"`
	SHA   string `json:"sha,omitempty"`
}

type pull struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	State   string  `json:"state"`
	HTMLURL string  `json:"html_url"`
	Head    pullRef `json:"head"`
	Base    pullRef `json:"base"`
}

func (f *forge) createPull(w http.ResponseWriter, r *http.Request) {
	repo, ok := f.authorizeAPI(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	owner := r.PathValue("owner")
	branch := body.Head
	if headOwner, ref, ok := strings.Cut(body.Head, ":"); ok {
		if headOwner != owner {
			validationFailed(w, "head", "invalid", "head must be a branch of "+repo)
			return
		}
		branch = ref
	}
	if !strings.HasPrefix(branch, branchPrefix) {
		validationFailed(w, "head", "invalid", "head must be a "+branchPrefix+"* branch")
		return
	}
	headSHA, ok := f.branchSHA(repo, branch)
	if !ok {
		validationFailed(w, "head", "invalid", "head branch "+branch+" does not exist")
		return
	}
	if body.Base == "" {
		validationFailed(w, "base", "missing_field", "base is required")
		return
	}
	if _, ok := f.branchSHA(repo, body.Base); !ok {
		validationFailed(w, "base", "invalid", "base branch "+body.Base+" does not exist")
		return
	}
	label := owner + ":" + branch
	f.mu.Lock()
	for _, p := range f.pulls[repo] {
		if p.State == "open" && p.Head.Label == label {
			f.mu.Unlock()
			validationFailed(w, "head", "custom", "A pull request already exists for "+label+".")
			return
		}
	}
	number := len(f.pulls[repo]) + 1
	p := pull{
		Number:  number,
		Title:   body.Title,
		State:   "open",
		HTMLURL: "http://" + r.Host + "/" + repo + "/pull/" + strconv.Itoa(number),
		Head:    pullRef{Ref: branch, Label: label, SHA: headSHA},
		Base:    pullRef{Ref: body.Base},
	}
	f.pulls[repo] = append(f.pulls[repo], p)
	f.mu.Unlock()
	log.Printf("fakeforge: pull request %s#%d from %s", repo, number, branch)
	writeJSON(w, http.StatusCreated, p)
}

func (f *forge) listPulls(w http.ResponseWriter, r *http.Request) {
	repo, ok := f.authorizeAPI(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "":
		state = "open"
	case "open", "closed", "all":
	default:
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed: state must be open, closed or all")
		return
	}
	head := r.URL.Query().Get("head")
	out := []pull{}
	f.mu.Lock()
	for _, p := range slices.Backward(f.pulls[repo]) {
		if state != "all" && p.State != state {
			continue
		}
		if head != "" && p.Head.Label != head && p.Head.Ref != head {
			continue
		}
		out = append(out, p)
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}
