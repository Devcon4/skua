package apiclienttest

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	s.mu.Lock()
	if s.refuse.times > 0 {
		s.refuse.times--
		f := s.refuse
		s.mu.Unlock()
		s.fail(w, r, f.code, f.reason)
		return
	}
	if s.streamOpen {
		s.mu.Unlock()
		s.fail(w, r, http.StatusConflict, "stream active")
		return
	}
	s.streamOpen = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.streamOpen = false
		s.mu.Unlock()
	}()
	sw, err := wire.NewWriter(w)
	if err != nil {
		return
	}
	s.record(r, http.StatusOK)
	last, _ := wire.LastEventID(r)
	written := 0
	hb := time.NewTicker(wire.HeartbeatInterval)
	defer hb.Stop()
	for {
		s.mu.Lock()
		pending := s.commandsAfter(last)
		changed := s.changed
		s.mu.Unlock()
		for _, c := range pending {
			if s.takeDrop(written) {
				cut(w, c)
			}
			if err := sw.Event(c.ID, c.Type, c.Data); err != nil {
				return
			}
			written++
			last = c.ID
		}
		select {
		case <-changed:
		case <-hb.C:
			if err := sw.Heartbeat(); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		}
	}
}

func (s *Server) commandsAfter(last int64) []wire.Command {
	if last >= int64(len(s.cmds)) {
		if last == int64(len(s.cmds)) {
			return nil
		}
		last = 0
	}
	return slices.Clone(s.cmds[last:])
}

func (s *Server) takeDrop(written int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropAfter == 0 || written < s.dropAfter {
		return false
	}
	s.dropAfter = 0
	return true
}

func cut(w http.ResponseWriter, c wire.Command) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s", c.ID, c.Type, c.Data[:len(c.Data)/2])
	_ = http.NewResponseController(w).Flush()
	panic(http.ErrAbortHandler)
}
