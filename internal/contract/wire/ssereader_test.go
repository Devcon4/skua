package wire

import (
	"errors"
	"io"
	"iter"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	sse "github.com/tmaxmax/go-sse"
)

func readAll(seq iter.Seq2[Message, error]) ([]Message, error) {
	var out []Message
	for m, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, m)
	}
	return out, nil
}

func TestWriterReaderRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := rec.Header().Get(k); got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	in := []Message{
		{ID: "1", Type: EventText, Data: []byte(`{"delta":"hi"}`)},
		{ID: "2", Type: EventToolEnd, Data: []byte("line one\nline two\n\n line four ")},
		{ID: "3", Type: EventEnd, Data: []byte("crlf\r\nand cr\rdone")},
	}
	for i, m := range in {
		id := int64(i + 1)
		if err := w.Heartbeat(); err != nil {
			t.Fatal(err)
		}
		if err := w.Event(id, m.Type, m.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if !rec.Flushed {
		t.Fatal("writer did not flush")
	}
	got, err := readAll(ReadEvents(rec.Body))
	if err != nil {
		t.Fatal(err)
	}
	in[2].Data = []byte("crlf\nand cr\ndone")
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("got  %q\nwant %q", got, in)
	}
}

func TestReaderLineEndings(t *testing.T) {
	want := []Message{
		{ID: "7", Type: "status", Data: []byte("a\nb")},
		{ID: "7", Type: "message", Data: []byte("c")},
	}
	for name, stream := range map[string]string{
		"crlf": "id: 7\r\nevent: status\r\ndata: a\r\ndata: b\r\n\r\n: hb\r\n\r\ndata: c\r\n\r\n",
		"lf":   "id: 7\nevent: status\ndata: a\ndata: b\n\n: hb\n\ndata: c\n\n",
		"cr":   "id: 7\revent: status\rdata: a\rdata: b\r\r: hb\r\rdata: c\r\r",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readAll(ReadEvents(strings.NewReader(stream)))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
		})
	}
}

func TestReaderCRLFAcrossReads(t *testing.T) {
	stream := "id: 1\r\ndata: x\r\n\r\ndata: y\r\n\r\n"
	got, err := readAll(ReadEvents(&oneByteReader{s: stream}))
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{{ID: "1", Type: "message", Data: []byte("x")}, {ID: "1", Type: "message", Data: []byte("y")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

type oneByteReader struct{ s string }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p[:1], r.s)
	r.s = r.s[n:]
	return n, nil
}

func TestReaderFieldRules(t *testing.T) {
	stream := "\uFEFFretry: 10\nunknown: x\ndata\ndata:no-space\ndata:  two-spaces\n\n" +
		"id: 5\nevent: only-id\n\n" +
		"data: after\n\n" +
		"id: 6\x00bad\ndata: nul id ignored\n\n" +
		"id\ndata: id reset\n\n"
	got, err := readAll(ReadEvents(strings.NewReader(stream)))
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{
		{ID: "", Type: "message", Data: []byte("\nno-space\n two-spaces")},
		{ID: "5", Type: "message", Data: []byte("after")},
		{ID: "5", Type: "message", Data: []byte("nul id ignored")},
		{ID: "", Type: "message", Data: []byte("id reset")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestReaderCutMidEvent(t *testing.T) {
	whole := []Message{{ID: "1", Type: "message", Data: []byte("whole")}}
	for name, c := range map[string]struct {
		stream string
		err    error
	}{
		"after a field":     {stream: "id: 1\ndata: whole\n\nid: 2\nevent: text\ndata: part\n"},
		"after a cr at end": {stream: "id: 1\ndata: whole\n\nid: 2\r"},
		"inside a line":     {stream: "id: 1\ndata: whole\n\nid: 2\ndata: pa", err: sse.ErrUnexpectedEOF},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readAll(ReadEvents(strings.NewReader(c.stream)))
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if !reflect.DeepEqual(got, whole) {
				t.Fatalf("got  %q\nwant %q", got, whole)
			}
		})
	}
}

func TestReaderCleanEOF(t *testing.T) {
	for name, stream := range map[string]string{
		"empty":              "",
		"after event":        "data: x\n\n",
		"after heartbeat":    "data: x\n\n: hb\n\n",
		"after lone comment": "data: x\n\n: hb\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readAll(ReadEvents(strings.NewReader(stream))); err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

func TestReaderStopsOnReadError(t *testing.T) {
	boom := errors.New("boom")
	r := io.MultiReader(strings.NewReader("data: x\n\ndata: y\n"), iotest.ErrReader(boom))
	got, err := readAll(ReadEvents(r))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if want := []Message{{Type: "message", Data: []byte("x")}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestReaderLargeEvent(t *testing.T) {
	big := strings.Repeat("x", MaxEventsBodyBytes)
	got, err := readAll(ReadEvents(strings.NewReader("data: " + big + "\n\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0].Data) != big {
		t.Fatalf("got %d events", len(got))
	}
}
