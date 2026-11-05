package api

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func TestCloseStreamsEndsEventStreams(t *testing.T) {
	s := newServer(nil)
	s.Handle("GET /stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw, err := wire.NewWriter(w)
		if err != nil {
			t.Error(err)
			return
		}
		if err := sw.Heartbeat(); err != nil {
			t.Error(err)
			return
		}
		<-r.Context().Done()
	}))
	ts := httptest.NewServer(s.trackStreams(s.mux))
	t.Cleanup(func() {
		s.closeStreams()
		ts.Close()
	})

	resp, err := ts.Client().Get(ts.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	if line, err := br.ReadString('\n'); err != nil || line != ": hb\n" {
		t.Fatalf("first line = %q, %v; want heartbeat", line, err)
	}
	s.closeStreams()
	if rest, err := io.ReadAll(br); err != nil || string(rest) != "\n" {
		t.Fatalf("after close read %q, %v; want end of stream", rest, err)
	}
}
