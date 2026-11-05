package wire

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWriterBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Event(42, EventText, []byte("{\"delta\":\"a\"}\r\nsecond\rthird\n\nfifth")); err != nil {
		t.Fatal(err)
	}
	if err := w.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	want := "id: 42\nevent: text\ndata: {\"delta\":\"a\"}\ndata: second\ndata: third\ndata: \ndata: fifth\n\n: hb\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("wire bytes\ngot  %q\nwant %q", got, want)
	}
}

func TestWriterFlushesEachEvent(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w, err := NewWriter(rw)
		if err != nil {
			t.Error(err)
			return
		}
		if err := w.Event(1, CommandAssign, []byte(`{"run":"r"}`)); err != nil {
			t.Error(err)
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	done := make(chan struct{})
	var m Message
	go func() {
		defer close(done)
		for m, err = range ReadEvents(resp.Body) {
			break
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("event not delivered before the handler returned")
	}
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "1" || m.Type != CommandAssign || string(m.Data) != `{"run":"r"}` {
		t.Fatalf("got %+v", m)
	}
}

func TestNewWriterNeedsFlusher(t *testing.T) {
	if _, err := NewWriter(noFlushWriter{h: http.Header{}}); err == nil {
		t.Fatal("want error for a writer without Flush")
	}
}

type noFlushWriter struct{ h http.Header }

func (w noFlushWriter) Header() http.Header { return w.h }

func (w noFlushWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w noFlushWriter) WriteHeader(int) {}

func TestWriterRejectsBadInput(t *testing.T) {
	w, err := NewWriter(httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Event(1, "bad\ntype", []byte("x")); err == nil {
		t.Fatal("want error for a type with a line break")
	}
	if err := w.Event(1, EventText, nil); err == nil {
		t.Fatal("want error for empty data")
	}
}

func TestLastEventID(t *testing.T) {
	cases := []struct {
		header string
		set    bool
		id     int64
		ok     bool
	}{
		{set: false, id: 0, ok: false},
		{header: "", set: true, id: 0, ok: false},
		{header: "0", set: true, id: 0, ok: true},
		{header: "42", set: true, id: 42, ok: true},
		{header: "abc", set: true, id: 0, ok: false},
		{header: "4x", set: true, id: 0, ok: false},
		{header: "1.5", set: true, id: 0, ok: false},
		{header: "-3", set: true, id: 0, ok: false},
		{header: "99999999999999999999", set: true, id: 0, ok: false},
	}
	for _, c := range cases {
		t.Run(c.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/events", nil)
			if c.set {
				r.Header.Set("Last-Event-ID", c.header)
			}
			id, ok := LastEventID(r)
			if id != c.id || ok != c.ok {
				t.Fatalf("LastEventID(%q) = %d, %v; want %d, %v", c.header, id, ok, c.id, c.ok)
			}
		})
	}
}
