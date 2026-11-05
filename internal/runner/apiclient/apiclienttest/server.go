// Package apiclienttest serves skua-api's runner endpoints over httptest with the faults a real api produces:
// dropped streams, refusals, failing event and result posts.
package apiclienttest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// Request is one request the server answered. Status is 0 when the server dropped the connection instead.
type Request struct {
	Method      string
	Path        string
	Token       string
	Version     string
	LastEventID string
	Status      int
	At          time.Time
}

// EventPost is one decoded POST /runner/events body with the status it got.
type EventPost struct {
	Body   wire.RunnerEventsRequest
	Status int
}

// ResultPost is one stored result and how many distinct events of its run were stored before it.
type ResultPost struct {
	Result       wire.RunnerResult
	StoredEvents int
}

type fault struct {
	code   int
	reason string
	times  int
}

// Server is a fake skua-api for one runner pod. Commands get ids from 1 in Send order.
type Server struct {
	URL string

	srv  *httptest.Server
	done chan struct{}

	mu         sync.Mutex
	cmds       []wire.Command
	changed    chan struct{}
	streamOpen bool
	dropAfter  int
	dropEvents int
	refuse     fault
	failEvents fault
	failResult fault
	maxResult  int
	requests   []Request
	eventPosts []EventPost
	events     map[string]map[int64]wire.RunnerEvent
	acks       map[string]int64
	results    []ResultPost
}

// New starts the server and closes it when t ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		done:    make(chan struct{}),
		changed: make(chan struct{}),
		events:  map[string]map[int64]wire.RunnerEvent{},
		acks:    map[string]int64{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+wire.RunnerStreamPath, s.stream)
	mux.HandleFunc("POST "+wire.RunnerEventsPath, s.postEvents)
	mux.HandleFunc("POST "+wire.RunnerResultPath, s.postResult)
	s.srv = httptest.NewServer(mux)
	s.URL = s.srv.URL
	t.Cleanup(func() {
		close(s.done)
		s.srv.Close()
	})
	return s
}

// Send queues cmd on the command stream with the next id and returns that id. Empty Data is sent as {}.
func (s *Server) Send(cmd wire.Command) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd.ID = int64(len(s.cmds)) + 1
	if len(cmd.Data) == 0 {
		cmd.Data = json.RawMessage("{}")
	}
	s.cmds = append(s.cmds, cmd)
	close(s.changed)
	s.changed = make(chan struct{})
	return cmd.ID
}

// DropStreamAfter makes the open or next stream, once it has written n commands, cut the connection in the
// middle of the following command. It fires once; n <= 0 disarms it.
func (s *Server) DropStreamAfter(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropAfter = max(n, 0)
}

// RefuseStream answers the next times stream requests with code and the plain-text reason.
func (s *Server) RefuseStream(code int, reason string, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = fault{code: code, reason: reason, times: times}
}

// FailEvents answers the next times event posts with code without storing them.
func (s *Server) FailEvents(code int, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failEvents = fault{code: code, reason: http.StatusText(code), times: times}
}

// DropEventsResponse stores the next times event posts and then cuts the connection instead of answering.
func (s *Server) DropEventsResponse(times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropEvents = times
}

// FailResult answers the next times result posts with code without storing them.
func (s *Server) FailResult(code int, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failResult = fault{code: code, reason: http.StatusText(code), times: times}
}

// MaxResultBytes makes a result whose JSON exceeds n bytes get 413 ResultTooLarge; n <= 0 removes the cap.
func (s *Server) MaxResultBytes(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxResult = n
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *Server) EventPosts() []EventPost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.eventPosts)
}

// Events returns the distinct events stored for run in seq order.
func (s *Server) Events(run string) []wire.RunnerEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := make([]wire.RunnerEvent, 0, len(s.events[run]))
	for _, e := range s.events[run] {
		evs = append(evs, e)
	}
	slices.SortFunc(evs, func(a, b wire.RunnerEvent) int { return int(a.Seq - b.Seq) })
	return evs
}

func (s *Server) Results() []ResultPost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.results)
}

func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case !ok || tok == "":
		s.fail(w, r, http.StatusUnauthorized, wire.RefusalTokenInvalid)
		return false
	case r.Header.Get(wire.HeaderRunnerVersion) == "":
		s.fail(w, r, http.StatusBadRequest, v1alpha1.ReasonRunnerUnsupported)
		return false
	}
	return true
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, reason string) {
	s.record(r, code)
	http.Error(w, reason, code)
}

func (s *Server) record(r *http.Request, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordLocked(r, status)
}

func (s *Server) recordLocked(r *http.Request, status int) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.requests = append(s.requests, Request{
		Method:      r.Method,
		Path:        r.URL.Path,
		Token:       tok,
		Version:     r.Header.Get(wire.HeaderRunnerVersion),
		LastEventID: r.Header.Get("Last-Event-ID"),
		Status:      status,
		At:          time.Now(),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
