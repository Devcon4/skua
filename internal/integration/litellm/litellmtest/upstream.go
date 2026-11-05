package litellmtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"k8s.io/utils/clock"
)

// RequestRecord is one entry of the GET /_test/requests log. Scenario is empty for an
// unmatched request; Fault is the fault applied to this attempt, nil when none was.
type RequestRecord struct {
	Time     time.Time `json:"time"`
	API      string    `json:"api"`
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Scenario string    `json:"scenario"`
	Turn     int       `json:"turn"`
	Fault    *Fault    `json:"fault,omitempty"`
	Status   int       `json:"status"`
}

const (
	apiOpenAI    = "openai"
	apiAnthropic = "anthropic"

	maxRequestBytes = 64 << 20
	argumentPiece   = 16
)

type modelGroup struct {
	name, provider, upstream string
	maxInputTokens           int
	inputCost, outputCost    float64
}

var modelGroups = []modelGroup{
	{"e2e-primary", "openai", "fake-primary", 128000, 1e-5, 2e-5},
	{"e2e-helper", "openai", "fake-helper", 128000, 1e-6, 2e-6},
	{"e2e-other", "openai", "fake-other", 128000, 1e-5, 2e-5},
	{"e2e-claude", "anthropic", "claude-fake", 200000, 3e-6, 1.5e-5},
}

type turnKey struct{ scenario, turn int }

type server struct {
	scenarios []Scenario
	clock     clock.PassiveClock

	mu       sync.Mutex
	attempts map[turnKey]int
	log      []RequestRecord
	seq      int
}

// NewServer serves the scripted scenarios on POST /v1/chat/completions (OpenAI) and
// POST /v1/messages (Anthropic), both with and without streaming. GET /v1/models and
// GET /model_group/info (LiteLLM shape, optional ?model_group= filter) list the model groups
// e2e-primary, e2e-helper, e2e-other (openai) and e2e-claude (anthropic) with the kind
// environment's prices. GET /_test/requests returns every request as []RequestRecord.
// POST /_test/reset clears the request log and every fault's attempt count, so faults fire again.
// Fault delays wait on clk; when clk is not a clock.Clock they poll clk.Now.
func NewServer(scenarios []Scenario, clk clock.PassiveClock) http.Handler {
	s := &server{scenarios: scenarios, clock: clk, attempts: map[turnKey]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	mux.HandleFunc("POST /v1/messages", s.messages)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /model_group/info", s.modelGroupInfo)
	mux.HandleFunc("GET /_test/requests", s.requests)
	mux.HandleFunc("POST /_test/reset", s.reset)
	return mux
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type conversation struct {
	api          string
	model        string
	stream       bool
	includeUsage bool
	messages     []message
	size         int
}

type toolCall struct {
	id, name, args string
}

type reply struct {
	seq    int
	record RequestRecord
	fault  *Fault
	text   string
	calls  []toolCall
	usage  Usage
	err    error
}

func decode(w http.ResponseWriter, r *http.Request, api string, v any) (int, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err == nil {
		err = json.Unmarshal(body, v)
	}
	if err != nil {
		writeError(w, api, http.StatusBadRequest, "fakellm: "+err.Error())
		return 0, false
	}
	return len(body), true
}

func (s *server) serve(w http.ResponseWriter, r *http.Request, c conversation) {
	rp := s.plan(c)
	if f := rp.fault; f != nil && f.FirstByteDelay > 0 && !s.wait(r.Context(), f.FirstByteDelay) {
		return
	}
	switch {
	case rp.err != nil:
		writeError(w, c.api, http.StatusInternalServerError, "fakellm: "+rp.err.Error())
	case rp.fault != nil && rp.fault.Status != 0:
		writeError(w, c.api, rp.fault.Status, fmt.Sprintf("fakellm: scripted %d", rp.fault.Status))
	case rp.fault != nil && rp.fault.DropAfterChunks > 0 && !c.stream:
		panic(http.ErrAbortHandler)
	case c.stream && c.api == apiOpenAI:
		streamOpenAI(newStreamWriter(w, rp.dropAfterChunks()), c, rp)
	case c.stream:
		streamAnthropic(newStreamWriter(w, rp.dropAfterChunks()), c, rp)
	case c.api == apiOpenAI:
		writeJSON(w, http.StatusOK, openAIMessage(c, rp))
	default:
		writeJSON(w, http.StatusOK, anthropicMessage(c, rp))
	}
}

func (rp reply) dropAfterChunks() int {
	if rp.fault == nil {
		return 0
	}
	return rp.fault.DropAfterChunks
}

func (s *server) wait(ctx context.Context, d time.Duration) bool {
	if c, ok := s.clock.(clock.Clock); ok {
		t := c.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C():
			return true
		case <-ctx.Done():
			return false
		}
	}
	deadline := s.clock.Now().Add(d)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for s.clock.Now().Before(deadline) {
		select {
		case <-tick.C:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func errorType(api string, status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusUnprocessableEntity:
		return "unprocessable_entity_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case api == apiAnthropic && (status == http.StatusServiceUnavailable || status == 529):
		return "overloaded_error"
	case api == apiAnthropic && status >= 500:
		return "api_error"
	case status >= 500:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

func writeError(w http.ResponseWriter, api string, status int, msg string) {
	typ := errorType(api, status)
	if api == apiAnthropic {
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}})
		return
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": typ, "param": nil, "code": fmt.Sprint(status)}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) models(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0, 2*len(modelGroups))
	for _, g := range modelGroups {
		for _, id := range []string{g.name, g.upstream} {
			data = append(data, map[string]any{"id": id, "object": "model", "created": 0, "owned_by": "fakellm"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *server) modelGroupInfo(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("model_group")
	data := make([]map[string]any, 0, len(modelGroups))
	for _, g := range modelGroups {
		if filter != "" && filter != g.name {
			continue
		}
		data = append(data, map[string]any{
			"model_group":               g.name,
			"providers":                 []string{g.provider},
			"max_input_tokens":          g.maxInputTokens,
			"max_output_tokens":         8192,
			"input_cost_per_token":      g.inputCost,
			"output_cost_per_token":     g.outputCost,
			"mode":                      "chat",
			"supports_function_calling": true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *server) requests(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := append(make([]RequestRecord, 0, len(s.log)), s.log...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *server) reset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.attempts = map[turnKey]int{}
	s.log = nil
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
