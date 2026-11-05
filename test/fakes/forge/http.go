package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

func (f *forge) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", f.mintToken)
	mux.HandleFunc("DELETE /installation/token", f.revokeToken)
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls", f.createPull)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls", f.listPulls)
	mux.HandleFunc("GET /_test/tokens", f.testTokens)
	mux.HandleFunc("POST /_test/tokens/{owner}/{repo}", f.testMintToken)
	mux.HandleFunc("GET /_test/refs/{owner}/{repo}", f.testRefs)
	mux.HandleFunc("GET /_test/pulls/{owner}/{repo}", f.testPulls)
	mux.HandleFunc("/{owner}/{repo}/{rest...}", f.serveGit)
	return mux
}

func bearer(r *http.Request) string {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || (!strings.EqualFold(scheme, "Bearer") && !strings.EqualFold(scheme, "token")) {
		return ""
	}
	return strings.TrimSpace(value)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("fakeforge: write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func validationFailed(w http.ResponseWriter, field, code, message string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"message": "Validation Failed",
		"errors":  []map[string]string{{"resource": "PullRequest", "field": field, "code": code, "message": message}},
	})
}
