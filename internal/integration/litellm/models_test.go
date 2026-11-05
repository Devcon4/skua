package litellm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Devcon4/skua/internal/integration/litellm"
)

func TestModelGroupsListsLargeGateway(t *testing.T) {
	type entry struct {
		ModelGroup     string      `json:"model_group"`
		Providers      []string    `json:"providers"`
		MaxInputTokens json.Number `json:"max_input_tokens,omitempty"`
		Padding        string      `json:"padding"`
	}
	pad := strings.Repeat("x", 1<<20)
	body, err := json.Marshal(map[string][]entry{"data": {
		{ModelGroup: "e2e-primary", Providers: []string{"openai"}, MaxInputTokens: "32768.0", Padding: pad},
		{ModelGroup: "e2e-claude", Providers: []string{"anthropic"}, Padding: pad},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var auth, query string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model_group/info" {
			http.NotFound(w, r)
			return
		}
		auth, query = r.Header.Get("Authorization"), r.URL.RawQuery
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)

	got, err := litellm.New(s.URL, value, s.Client()).ModelGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []litellm.ModelGroup{
		{Name: "e2e-primary", Providers: []string{"openai"}, MaxInputTokens: 32768},
		{Name: "e2e-claude", Providers: []string{"anthropic"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelGroups = %+v, want %+v", got, want)
	}
	if auth != "Bearer "+value || query != "" {
		t.Fatalf("auth = %q, query = %q", auth, query)
	}
}

func TestModelGroupsErrorOmitsKey(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Error, Invalid proxy server token passed.","type":"auth_error","param":"None","code":"401"}}`))
	}))
	t.Cleanup(s.Close)

	_, err := litellm.New(s.URL, value, s.Client()).ModelGroups(context.Background())
	var apiErr *litellm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Path != "/model_group/info" {
		t.Fatalf("err = %v, want APIError 401 on /model_group/info", err)
	}
	if strings.Contains(err.Error(), value) {
		t.Fatalf("error %q contains the key", err)
	}
}
