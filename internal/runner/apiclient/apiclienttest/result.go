package apiclienttest

import (
	"encoding/json"
	"net/http"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func (s *Server) postResult(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	var res wire.RunnerResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil || res.Run == "" {
		s.fail(w, r, http.StatusBadRequest, "invalid result body")
		return
	}
	s.mu.Lock()
	if s.failResult.times > 0 {
		s.failResult.times--
		f := s.failResult
		s.mu.Unlock()
		s.fail(w, r, f.code, f.reason)
		return
	}
	if s.maxResult > 0 && len(res.Result) > s.maxResult {
		s.mu.Unlock()
		s.fail(w, r, http.StatusRequestEntityTooLarge, "ResultTooLarge")
		return
	}
	s.results = append(s.results, ResultPost{Result: res, StoredEvents: len(s.events[res.Run])})
	s.recordLocked(r, http.StatusOK)
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}
