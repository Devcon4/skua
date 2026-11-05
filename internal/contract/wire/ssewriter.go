package wire

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	sse "github.com/tmaxmax/go-sse"
)

const lastEventIDHeader = "Last-Event-ID"

// Writer writes a text/event-stream response and flushes after every write. It is safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

// NewWriter sets the event-stream headers and flushes them. It fails when w cannot flush.
func NewWriter(w http.ResponseWriter) (*Writer, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return nil, fmt.Errorf("wire: event stream needs a flushing response writer: %w", err)
	}
	return &Writer{w: w, rc: rc}, nil
}

// Event writes one event. data must be non-empty, because a reader drops events with empty data;
// line breaks in data become separate data lines and a trailing line break is dropped.
func (sw *Writer) Event(id int64, typ string, data []byte) error {
	t, err := sse.NewType(typ)
	if err != nil {
		return fmt.Errorf("wire: event type %q: %w", typ, err)
	}
	if len(data) == 0 {
		return errors.New("wire: event data is empty")
	}
	m := &sse.Message{ID: sse.ID(strconv.FormatInt(id, 10)), Type: t}
	m.AppendData(string(data))
	return sw.send(m)
}

// Heartbeat writes the comment line ": hb".
func (sw *Writer) Heartbeat() error {
	m := &sse.Message{}
	m.AppendComment("hb")
	return sw.send(m)
}

func (sw *Writer) send(m *sse.Message) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if _, err := m.WriteTo(sw.w); err != nil {
		return err
	}
	return sw.rc.Flush()
}

// LastEventID parses the Last-Event-ID header as a non-negative integer; false when absent or invalid.
func LastEventID(r *http.Request) (int64, bool) {
	v := r.Header.Get(lastEventIDHeader)
	if v == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id < 0 {
		return 0, false
	}
	return id, true
}
