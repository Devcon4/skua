package main

import (
	"net/http"
	"time"
)

func (f *forge) testTokens(w http.ResponseWriter, _ *http.Request) {
	type tokenView struct {
		SHA256Prefix string    `json:"sha256Prefix"`
		Repo         string    `json:"repo"`
		ExpiresAt    time.Time `json:"expiresAt"`
		Revoked      bool      `json:"revoked"`
		Pushes       int       `json:"pushes"`
	}
	f.mu.Lock()
	out := make([]tokenView, 0, len(f.tokens))
	for _, rec := range f.tokens {
		out = append(out, tokenView{SHA256Prefix: shaPrefix(rec.value), Repo: rec.repo, ExpiresAt: rec.expiresAt, Revoked: rec.revoked, Pushes: rec.pushes})
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (f *forge) testMintToken(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	if !f.repos[repo] {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	rec, err := f.mint(repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": rec.value, "expiresAt": rec.expiresAt})
}

func (f *forge) testRefs(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	if !f.repos[repo] {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	refs, err := f.refs(repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, refs)
}

func (f *forge) testPulls(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	if !f.repos[repo] {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	f.mu.Lock()
	out := append([]pull{}, f.pulls[repo]...)
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}
