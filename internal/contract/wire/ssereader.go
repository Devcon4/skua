package wire

import (
	"errors"
	"io"
	"iter"

	sse "github.com/tmaxmax/go-sse"
)

// maxEventBytes lifts go-sse's 64 KiB default above the 1 MiB event data cap.
const maxEventBytes = 4 << 20

// Message is one dispatched event. ID is the stream's last event ID, which persists across events.
type Message struct {
	ID   string
	Type string
	Data []byte
}

// ReadEvents yields the events with non-empty data from an event stream. Iteration ends without an error
// when the stream ends; an event cut off by the end of the stream is discarded, and one cut inside a
// line yields sse.ErrUnexpectedEOF. An event without a type has type "message".
func ReadEvents(r io.Reader) iter.Seq2[Message, error] {
	return func(yield func(Message, error) bool) {
		for ev, err := range sse.Read(endReader{r}, &sse.ReadConfig{MaxEventSize: maxEventBytes}) {
			if errors.Is(err, errStreamEnd) {
				return
			}
			if err != nil {
				yield(Message{}, err)
				return
			}
			if ev.Data == "" {
				continue
			}
			if ev.Type == "" {
				ev.Type = "message"
			}
			if !yield(Message{ID: ev.LastEventID, Type: ev.Type, Data: []byte(ev.Data)}, nil) {
				return
			}
		}
	}
}

// errStreamEnd hides io.EOF from sse.Read, which would otherwise dispatch a trailing event with no blank line.
var errStreamEnd = errors.New("wire: event stream ended")

type endReader struct{ r io.Reader }

func (e endReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		err = errStreamEnd
	}
	return n, err
}
