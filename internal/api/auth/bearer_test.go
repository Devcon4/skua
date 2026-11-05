package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerToken(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer tok":     "tok",
		"bearer tok":     "tok",
		"BEARER  tok ":   "tok",
		"":               "",
		"Bearer":         "",
		"Bearer ":        "",
		"Basic dXNlcjpw": "",
		"Bearertok":      "",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		got, ok := BearerToken(r)
		if ok != (want != "") || ok && got != want {
			t.Errorf("BearerToken(%q) = %q, %v; want %q", header, got, ok, want)
		}
	}
}
