package api

import (
	"context"
	"mime"
	"net/http"
)

func (s *Server) trackStreams(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		sw := &streamWriter{ResponseWriter: w, server: s, cancel: cancel}
		defer s.untrack(sw)
		h.ServeHTTP(sw, r.WithContext(ctx))
	})
}

func (s *Server) track(sw *streamWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		sw.cancel()
		return
	}
	s.streams[sw] = struct{}{}
}

func (s *Server) untrack(sw *streamWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streams, sw)
}

func (s *Server) closeStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
	for sw := range s.streams {
		sw.cancel()
	}
	clear(s.streams)
}

type streamWriter struct {
	http.ResponseWriter
	server  *Server
	cancel  context.CancelFunc
	checked bool
}

func (w *streamWriter) detect() {
	if w.checked {
		return
	}
	w.checked = true
	if mt, _, err := mime.ParseMediaType(w.Header().Get("Content-Type")); err == nil && mt == "text/event-stream" {
		w.server.track(w)
	}
}

func (w *streamWriter) WriteHeader(code int) {
	w.detect()
	w.ResponseWriter.WriteHeader(code)
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.detect()
	return w.ResponseWriter.Write(p)
}

func (w *streamWriter) FlushError() error {
	w.detect()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *streamWriter) Flush() {
	_ = w.FlushError()
}

func (w *streamWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
