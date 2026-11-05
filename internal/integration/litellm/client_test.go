package litellm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/Devcon4/skua/internal/integration/litellm"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
)

const (
	operatorKey = "sk-operator-test-0001"
	alias       = "skua-run-0001"
	value       = "sk-skua-run-0001"
	otherValue  = "sk-skua-run-0002"
)

func mintRequest() litellm.MintRequest {
	return litellm.MintRequest{
		Key:       value,
		Alias:     alias,
		Models:    []string{"ollama-primary", "ollama-helper"},
		MaxBudget: 2,
		Duration:  65 * time.Minute,
		Metadata:  map[string]string{"run": "p-1", "namespace": "skua", "profile": "p", "owner": "abc"},
	}
}

type seeder interface {
	addKey(alias, value string)
	addForeignKey(alias, value string)
	addModel(name string, priced bool, providers ...string)
}

type implementation struct {
	name  string
	start func(t *testing.T) (litellm.Provider, seeder)
}

var implementations = []implementation{
	{"client", func(t *testing.T) (litellm.Provider, seeder) {
		s := newReplayServer(t)
		return litellm.New(s.URL+"/", operatorKey, s.Client()), s
	}},
	{"litellmtest", func(t *testing.T) (litellm.Provider, seeder) {
		f := litellmtest.New(clocktesting.NewFakePassiveClock(time.Unix(1_800_000_000, 0)))
		return f, fakeSeeder{f}
	}},
}

type fakeSeeder struct{ f *litellmtest.Fake }

func (s fakeSeeder) addKey(alias, value string) {
	r := mintRequest()
	r.Alias, r.Key = alias, value
	if err := s.f.Mint(context.Background(), r); err != nil {
		panic(err)
	}
}

func (s fakeSeeder) addForeignKey(alias, value string) { s.f.AddForeignKey(alias, value) }

func (s fakeSeeder) addModel(name string, priced bool, providers ...string) {
	s.f.AddModel(name, priced, providers...)
}

var behaviours = []struct {
	name  string
	seed  func(s seeder)
	check func(t *testing.T, p litellm.Provider)
}{
	{
		name: "first mint succeeds",
		check: func(t *testing.T, p litellm.Provider) {
			mustNil(t, p.Mint(context.Background(), mintRequest()))
		},
	},
	{
		name: "identical retry confirms",
		check: func(t *testing.T, p litellm.Provider) {
			mustNil(t, p.Mint(context.Background(), mintRequest()))
			mustNil(t, p.Mint(context.Background(), mintRequest()))
		},
	},
	{
		name: "alias held by another value conflicts",
		seed: func(s seeder) { s.addKey(alias, otherValue) },
		check: func(t *testing.T, p litellm.Provider) {
			mustIs(t, p.Mint(context.Background(), mintRequest()), litellm.ErrAliasConflict)
		},
	},
	{
		name: "value held under another alias conflicts when the alias is taken",
		seed: func(s seeder) {
			s.addKey(alias, otherValue)
			s.addKey("skua-run-0003", value)
		},
		check: func(t *testing.T, p litellm.Provider) {
			mustIs(t, p.Mint(context.Background(), mintRequest()), litellm.ErrAliasConflict)
		},
	},
	{
		name: "alias held by another owner conflicts",
		seed: func(s seeder) { s.addForeignKey(alias, otherValue) },
		check: func(t *testing.T, p litellm.Provider) {
			mustIs(t, p.Mint(context.Background(), mintRequest()), litellm.ErrAliasConflict)
		},
	},
	{
		name: "mint without models is refused",
		check: func(t *testing.T, p litellm.Provider) {
			r := mintRequest()
			r.Models = nil
			mustErr(t, p.Mint(context.Background(), r))
		},
	},
	{
		name: "mint with a short value is refused",
		check: func(t *testing.T, p litellm.Provider) {
			r := mintRequest()
			r.Key = "sk-short"
			mustErr(t, p.Mint(context.Background(), r))
		},
	},
	{
		name: "revoke existing alias then mint again",
		seed: func(s seeder) { s.addKey(alias, value) },
		check: func(t *testing.T, p litellm.Provider) {
			mustNil(t, p.Revoke(context.Background(), alias))
			mustNil(t, p.Mint(context.Background(), mintRequest()))
		},
	},
	{
		name: "revoke after revoke succeeds",
		seed: func(s seeder) { s.addKey(alias, value) },
		check: func(t *testing.T, p litellm.Provider) {
			mustNil(t, p.Revoke(context.Background(), alias))
			mustNil(t, p.Revoke(context.Background(), alias))
		},
	},
	{
		name: "revoke missing alias succeeds",
		check: func(t *testing.T, p litellm.Provider) {
			mustNil(t, p.Revoke(context.Background(), alias))
		},
	},
	{
		name: "revoke another owner's key",
		seed: func(s seeder) { s.addForeignKey(alias, otherValue) },
		check: func(t *testing.T, p litellm.Provider) {
			mustIs(t, p.Revoke(context.Background(), alias), litellm.ErrNotOwner)
		},
	},
	{
		name: "model known and priced",
		seed: func(s seeder) { s.addModel("ollama-primary", true, "openai") },
		check: func(t *testing.T, p litellm.Provider) {
			mustModel(t, p, "ollama-primary", litellm.ModelInfo{Known: true, Priced: true, Providers: []string{"openai"}})
		},
	},
	{
		name: "model unknown",
		seed: func(s seeder) { s.addModel("ollama-primary", true, "openai") },
		check: func(t *testing.T, p litellm.Provider) {
			mustModel(t, p, "claude-nope", litellm.ModelInfo{})
		},
	},
	{
		name: "model known but unpriced",
		seed: func(s seeder) { s.addModel("claude-haiku-4-5", false, "anthropic") },
		check: func(t *testing.T, p litellm.Provider) {
			mustModel(t, p, "claude-haiku-4-5", litellm.ModelInfo{Known: true, Providers: []string{"anthropic"}})
		},
	},
}

func TestBehaviour(t *testing.T) {
	for _, impl := range implementations {
		for _, b := range behaviours {
			t.Run(impl.name+"/"+b.name, func(t *testing.T) {
				p, s := impl.start(t)
				if b.seed != nil {
					b.seed(s)
				}
				b.check(t, p)
			})
		}
	}
}

func TestMintRequestBody(t *testing.T) {
	s := newReplayServer(t)
	p := litellm.New(s.URL, operatorKey, s.Client())
	mustNil(t, p.Mint(context.Background(), mintRequest()))

	reqs := s.recorded()
	if len(reqs) != 1 || reqs[0].path != "/key/generate" || reqs[0].method != http.MethodPost {
		t.Fatalf("requests = %+v", reqs)
	}
	if got := reqs[0].auth; got != "Bearer "+operatorKey {
		t.Fatalf("Authorization = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[0].body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["user_id"]; ok {
		t.Fatalf("body has user_id: %s", reqs[0].body)
	}
	want := map[string]any{
		"key":        value,
		"key_alias":  alias,
		"key_type":   "llm_api",
		"models":     []any{"ollama-primary", "ollama-helper"},
		"max_budget": 2.0,
		"duration":   "3900s",
		"metadata":   map[string]any{"run": "p-1", "namespace": "skua", "profile": "p", "owner": "abc"},
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v, want %v", body, want)
	}
}

func TestMintDurationRoundsUp(t *testing.T) {
	s := newReplayServer(t)
	p := litellm.New(s.URL, operatorKey, s.Client())
	r := mintRequest()
	r.Duration = 1500 * time.Millisecond
	mustNil(t, p.Mint(context.Background(), r))
	var body struct {
		Duration string `json:"duration"`
	}
	if err := json.Unmarshal(s.recorded()[0].body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Duration != "2s" {
		t.Fatalf("duration = %q", body.Duration)
	}
}

func TestMintRetryReadsKeyInfo(t *testing.T) {
	s := newReplayServer(t)
	s.addKey(alias, value)
	p := litellm.New(s.URL, operatorKey, s.Client())
	mustNil(t, p.Mint(context.Background(), mintRequest()))

	reqs := s.recorded()
	if len(reqs) != 2 || reqs[1].method != http.MethodGet || reqs[1].path != "/key/info" || reqs[1].query != "key="+value {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[1].auth != "Bearer "+operatorKey {
		t.Fatalf("Authorization = %q", reqs[1].auth)
	}
}

func TestRevokeRequestBody(t *testing.T) {
	s := newReplayServer(t)
	s.addKey(alias, value)
	p := litellm.New(s.URL, operatorKey, s.Client())
	mustNil(t, p.Revoke(context.Background(), alias))

	reqs := s.recorded()
	if len(reqs) != 1 || reqs[0].path != "/key/delete" || reqs[0].auth != "Bearer "+operatorKey {
		t.Fatalf("requests = %+v", reqs)
	}
	if got := string(reqs[0].body); got != `{"key_aliases":["skua-run-0001"]}` {
		t.Fatalf("body = %s", got)
	}
}

func TestModelInfoQuery(t *testing.T) {
	s := newReplayServer(t)
	p := litellm.New(s.URL, operatorKey, s.Client())
	if _, err := p.ModelInfo(context.Background(), "wild/qwen3:14b"); err != nil {
		t.Fatal(err)
	}
	reqs := s.recorded()
	if len(reqs) != 1 || reqs[0].path != "/model_group/info" || reqs[0].query != "model_group=wild%2Fqwen3%3A14b" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestErrorResponses(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		status     int
		body       string
		call       func(p litellm.Provider) error
		wantStatus int
		wantType   string
	}{
		{
			name:       "mint over budget cap",
			path:       "/key/generate",
			status:     http.StatusBadRequest,
			body:       `{"error":{"message":"max_budget is over max limit set in config - user_value=100.0; max_value=25.0","type":"bad_request_error","param":null,"code":"400"}}`,
			call:       func(p litellm.Provider) error { return p.Mint(context.Background(), mintRequest()) },
			wantStatus: http.StatusBadRequest,
			wantType:   "bad_request_error",
		},
		{
			name:       "mint with a management key",
			path:       "/key/generate",
			status:     http.StatusUnauthorized,
			body:       `{"error":{"message":"Only proxy admin can be used to generate, delete, update info for new keys/users/teams.","type":"auth_error","param":"None","code":"401"}}`,
			call:       func(p litellm.Provider) error { return p.Mint(context.Background(), mintRequest()) },
			wantStatus: http.StatusUnauthorized,
			wantType:   "auth_error",
		},
		{
			name:       "mint server error",
			path:       "/key/generate",
			status:     http.StatusInternalServerError,
			body:       `Internal Server Error`,
			call:       func(p litellm.Provider) error { return p.Mint(context.Background(), mintRequest()) },
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "revoke route missing",
			path:       "/key/delete",
			status:     http.StatusNotFound,
			body:       `{"detail":"Not Found"}`,
			call:       func(p litellm.Provider) error { return p.Revoke(context.Background(), alias) },
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "model info server error",
			path:       "/model_group/info",
			status:     http.StatusServiceUnavailable,
			body:       `{"error":{"message":"db unavailable","type":"internal_server_error","param":"None","code":"503"}}`,
			call:       func(p litellm.Provider) error { _, err := p.ModelInfo(context.Background(), "m"); return err },
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "internal_server_error",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newReplayServer(t)
			s.override(c.path, c.status, c.body)
			err := c.call(litellm.New(s.URL, operatorKey, s.Client()))
			var apiErr *litellm.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if apiErr.Status != c.wantStatus || apiErr.Type != c.wantType || apiErr.Path != c.path {
				t.Fatalf("err = %+v", apiErr)
			}
			if errors.Is(err, litellm.ErrAliasConflict) || errors.Is(err, litellm.ErrNotOwner) {
				t.Fatalf("err = %v", err)
			}
			if n := len(s.recorded()); n != 1 {
				t.Fatalf("%d requests, want 1", n)
			}
		})
	}
}

func TestKeyInfoErrorAfterAliasRefusal(t *testing.T) {
	s := newReplayServer(t)
	s.addKey(alias, value)
	s.override("/key/info", http.StatusForbidden, `{"error":{"message":"You are not authorized to view this key","type":"auth_error","param":"None","code":"403"}}`)
	err := litellm.New(s.URL, operatorKey, s.Client()).Mint(context.Background(), mintRequest())
	var apiErr *litellm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Path != "/key/info" {
		t.Fatalf("err = %v", err)
	}
}

func TestMintResponseWithOtherKey(t *testing.T) {
	s := newReplayServer(t)
	s.override("/key/generate", http.StatusOK, `{"key":"sk-generated-by-litellm","key_alias":"skua-run-0001"}`)
	mustErr(t, litellm.New(s.URL, operatorKey, s.Client()).Mint(context.Background(), mintRequest()))
}

func TestTransportErrorHidesKeyValue(t *testing.T) {
	s := newReplayServer(t)
	s.addKey(alias, value)
	s.hangUp("/key/info")
	err := litellm.New(s.URL, operatorKey, s.Client()).Mint(context.Background(), mintRequest())
	if err == nil {
		t.Fatal("err = nil")
	}
	if strings.Contains(err.Error(), value) {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCancelled(t *testing.T) {
	s := newReplayServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mustIs(t, litellm.New(s.URL, operatorKey, s.Client()).Revoke(ctx, alias), context.Canceled)
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}

func mustErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil")
	}
}

func mustIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

func mustModel(t *testing.T, p litellm.Provider, model string, want litellm.ModelInfo) {
	t.Helper()
	got, err := p.ModelInfo(context.Background(), model)
	mustNil(t, err)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelInfo(%q) = %+v, want %+v", model, got, want)
	}
}

type recordedRequest struct {
	method, path, query, auth string
	body                      []byte
}

type replayKey struct {
	alias   string
	foreign bool
}

type replayModel struct {
	priced    bool
	providers []string
}

type cannedResponse struct {
	status int
	body   string
}

type replayServer struct {
	*httptest.Server

	mu        sync.Mutex
	keys      map[string]replayKey
	models    map[string]replayModel
	overrides map[string]cannedResponse
	hangUps   map[string]bool
	requests  []recordedRequest
}

func newReplayServer(t *testing.T) *replayServer {
	s := &replayServer{
		keys:      map[string]replayKey{},
		models:    map[string]replayModel{},
		overrides: map[string]cannedResponse{},
		hangUps:   map[string]bool{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *replayServer) addKey(alias, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[value] = replayKey{alias: alias}
}

func (s *replayServer) addForeignKey(alias, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[value] = replayKey{alias: alias, foreign: true}
}

func (s *replayServer) addModel(name string, priced bool, providers ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models[name] = replayModel{priced: priced, providers: providers}
}

func (s *replayServer) override(path string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides[path] = cannedResponse{status, body}
}

func (s *replayServer) hangUp(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hangUps[path] = true
}

func (s *replayServer) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func (s *replayServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, recordedRequest{
		method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"), body: body,
	})
	if s.hangUps[r.URL.Path] {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if c, ok := s.overrides[r.URL.Path]; ok {
		reply(w, c.status, c.body)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+operatorKey {
		reply(w, http.StatusUnauthorized, `{"error":{"message":"Authentication Error, Invalid proxy server token passed.","type":"auth_error","param":"None","code":"401"}}`)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/key/generate":
		s.generate(w, body)
	case r.Method == http.MethodGet && r.URL.Path == "/key/info":
		s.info(w, r.URL.Query().Get("key"))
	case r.Method == http.MethodPost && r.URL.Path == "/key/delete":
		s.delete(w, body)
	case r.Method == http.MethodGet && r.URL.Path == "/model_group/info":
		s.modelGroup(w, r.URL.Query().Get("model_group"))
	default:
		reply(w, http.StatusNotFound, `{"detail":"Not Found"}`)
	}
}

func (s *replayServer) generate(w http.ResponseWriter, body []byte) {
	var req struct {
		Key      string   `json:"key"`
		KeyAlias string   `json:"key_alias"`
		Models   []string `json:"models"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		reply(w, http.StatusUnprocessableEntity, `{"detail":"invalid body"}`)
		return
	}
	for _, k := range s.keys {
		if k.alias == req.KeyAlias {
			reply(w, http.StatusBadRequest, fmt.Sprintf(`{"error":{"message":"Key with alias '%s' already exists. Unique key aliases across all keys are required.","type":"bad_request_error","param":"key_alias","code":"400"}}`, req.KeyAlias))
			return
		}
	}
	if _, ok := s.keys[req.Key]; !ok {
		s.keys[req.Key] = replayKey{alias: req.KeyAlias}
	}
	models, _ := json.Marshal(req.Models)
	reply(w, http.StatusOK, fmt.Sprintf(`{"key_alias":%q,"duration":"3900s","models":%s,"spend":0.0,"max_budget":2.0,"user_id":"skua-op","team_id":null,"key_type":"llm_api","key":%q,"key_name":"sk-...%s","expires":"2027-01-15T09:05:00.000000Z","token_id":"4f1c0e3a9b"}`, req.KeyAlias, models, req.Key, req.Key[len(req.Key)-4:]))
}

func (s *replayServer) info(w http.ResponseWriter, value string) {
	k, ok := s.keys[value]
	if !ok {
		reply(w, http.StatusNotFound, `{"error":{"message":"Key not found in database.","type":"not_found_error","param":"key","code":"404"}}`)
		return
	}
	if k.foreign {
		reply(w, http.StatusForbidden, `{"error":{"message":"You are not authorized to access this key's info.","type":"auth_error","param":"None","code":"403"}}`)
		return
	}
	reply(w, http.StatusOK, fmt.Sprintf(`{"key":%q,"info":{"key_name":"sk-...%s","key_alias":%q,"spend":0.0,"max_budget":2.0,"models":["ollama-primary","ollama-helper"],"user_id":"skua-op","expires":"2027-01-15T09:05:00Z"}}`, value, value[len(value)-4:], k.alias))
}

func (s *replayServer) delete(w http.ResponseWriter, body []byte) {
	var req struct {
		KeyAliases []string `json:"key_aliases"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.KeyAliases) != 1 {
		reply(w, http.StatusUnprocessableEntity, `{"detail":"invalid body"}`)
		return
	}
	for v, k := range s.keys {
		if k.alias != req.KeyAliases[0] {
			continue
		}
		if k.foreign {
			reply(w, http.StatusForbidden, `{"error":{"message":"You are not authorized to delete this key","type":"auth_error","param":"None","code":"403"}}`)
			return
		}
		delete(s.keys, v)
		reply(w, http.StatusOK, fmt.Sprintf(`{"deleted_keys":[%q]}`, k.alias))
		return
	}
	reply(w, http.StatusNotFound, `{"error":{"message":"{'error': 'No keys found'}","type":"internal_server_error","param":"None","code":"404"}}`)
}

func (s *replayServer) modelGroup(w http.ResponseWriter, name string) {
	m, ok := s.models[name]
	if !ok {
		reply(w, http.StatusOK, `{"data":[]}`)
		return
	}
	cost := "null"
	if m.priced {
		cost = "1e-05"
	}
	providers, _ := json.Marshal(m.providers)
	reply(w, http.StatusOK, fmt.Sprintf(`{"data":[{"model_group":%q,"providers":%s,"max_input_tokens":16384.0,"max_output_tokens":null,"input_cost_per_token":%s,"output_cost_per_token":%s,"mode":"chat","tpm":null,"rpm":null,"supports_parallel_function_calling":false,"supports_vision":false,"supports_function_calling":true,"supported_openai_params":["temperature","max_tokens","tools","tool_choice","stream"],"configurable_clientside_auth_params":null}]}`, name, providers, cost, strings.Replace(cost, "1e-05", "2e-05", 1)))
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
