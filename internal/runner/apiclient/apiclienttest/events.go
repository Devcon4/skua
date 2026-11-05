package apiclienttest

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	var req wire.RunnerEventsRequest
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, wire.MaxEventsBodyBytes)).Decode(&req)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		s.fail(w, r, http.StatusRequestEntityTooLarge, wire.RefusalBodyTooLarge)
		return
	}
	if err != nil || req.Run == "" {
		s.fail(w, r, http.StatusBadRequest, "invalid events body")
		return
	}
	s.mu.Lock()
	if s.failEvents.times > 0 {
		s.failEvents.times--
		f := s.failEvents
		s.eventPosts = append(s.eventPosts, EventPost{Body: req, Status: f.code})
		s.mu.Unlock()
		s.fail(w, r, f.code, f.reason)
		return
	}
	stored := s.events[req.Run]
	if stored == nil {
		stored = map[int64]wire.RunnerEvent{}
		s.events[req.Run] = stored
	}
	for _, e := range req.Events {
		if _, ok := stored[e.Seq]; !ok {
			stored[e.Seq] = e
			s.acks[req.Run] = max(s.acks[req.Run], e.Seq)
		}
	}
	ack := s.acks[req.Run]
	drop := s.dropEvents > 0
	status := http.StatusOK
	if drop {
		s.dropEvents--
		status = 0
	}
	s.eventPosts = append(s.eventPosts, EventPost{Body: req, Status: status})
	s.recordLocked(r, status)
	s.mu.Unlock()
	if drop {
		panic(http.ErrAbortHandler)
	}
	writeJSON(w, wire.Ack{Ack: ack})
}
