package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type tokenRecord struct {
	value     string
	repo      string
	expiresAt time.Time
	revoked   bool
	pushes    int
}

func shaPrefix(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:shaPrefixHex]
}

func newTokenValue() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ghs_" + hex.EncodeToString(b), nil
}

func (f *forge) liveToken(value string) (*tokenRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.byValue[value]
	if !ok || rec.revoked || !f.now().Before(rec.expiresAt) {
		return nil, false
	}
	return rec, true
}

func (f *forge) repoByName(name string) (string, bool) {
	for _, repo := range seedRepos {
		if _, n, _ := strings.Cut(repo, "/"); n == name {
			return repo, true
		}
	}
	return "", false
}

func (f *forge) mintToken(w http.ResponseWriter, r *http.Request) {
	if problem := f.jwtProblem(bearer(r)); problem != "" {
		writeError(w, http.StatusUnauthorized, problem)
		return
	}
	var body struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	if len(body.Repositories) != 1 {
		writeError(w, http.StatusUnprocessableEntity, "fakeforge mints tokens for exactly one repository")
		return
	}
	repo, ok := f.repoByName(body.Repositories[0])
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "There is at least one repository that does not exist or is not accessible to the parent installation.")
		return
	}
	rec, err := f.mint(repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, name, _ := strings.Cut(repo, "/")
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":                rec.value,
		"expires_at":           rec.expiresAt.Format(time.RFC3339),
		"permissions":          map[string]string{"contents": "write", "pull_requests": "write"},
		"repository_selection": "selected",
		"repositories":         []map[string]string{{"name": name, "full_name": repo}},
	})
}

func (f *forge) mint(repo string) (*tokenRecord, error) {
	value, err := newTokenValue()
	if err != nil {
		return nil, err
	}
	rec := &tokenRecord{value: value, repo: repo, expiresAt: f.now().Add(f.ttl).UTC().Truncate(time.Second)}
	f.mu.Lock()
	f.tokens = append(f.tokens, rec)
	f.byValue[value] = rec
	f.mu.Unlock()
	log.Printf("fakeforge: minted %s for %s until %s", shaPrefix(value), repo, rec.expiresAt.Format(time.RFC3339))
	return rec, nil
}

func (f *forge) revokeToken(w http.ResponseWriter, r *http.Request) {
	rec, ok := f.liveToken(bearer(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	f.mu.Lock()
	rec.revoked = true
	f.mu.Unlock()
	log.Printf("fakeforge: revoked %s for %s", shaPrefix(rec.value), rec.repo)
	w.WriteHeader(http.StatusNoContent)
}

func (f *forge) authorizeAPI(w http.ResponseWriter, r *http.Request) (string, bool) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	if !f.repos[repo] {
		writeError(w, http.StatusNotFound, "Not Found")
		return "", false
	}
	rec, ok := f.liveToken(bearer(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "Bad credentials")
		return "", false
	}
	if rec.repo != repo {
		writeError(w, http.StatusForbidden, "Resource not accessible by integration")
		return "", false
	}
	return repo, true
}
