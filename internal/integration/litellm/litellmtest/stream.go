package litellmtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf8"
)

type streamWriter struct {
	w     http.ResponseWriter
	rc    *http.ResponseController
	drop  int
	sent  int
	ready bool
}

func newStreamWriter(w http.ResponseWriter, drop int) *streamWriter {
	return &streamWriter{w: w, rc: http.NewResponseController(w), drop: drop}
}

func (sw *streamWriter) send(event string, data any) {
	if sw.drop > 0 && sw.sent == sw.drop {
		_ = sw.rc.Flush()
		panic(http.ErrAbortHandler)
	}
	if !sw.ready {
		h := sw.w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		sw.w.WriteHeader(http.StatusOK)
		sw.ready = true
	}
	var payload []byte
	if s, ok := data.(string); ok {
		payload = []byte(s)
	} else {
		payload, _ = json.Marshal(data)
	}
	if event != "" {
		_, _ = fmt.Fprintf(sw.w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(sw.w, "data: %s\n\n", payload)
	_ = sw.rc.Flush()
	sw.sent++
}

func finishReason(api string, rp reply) string {
	switch {
	case api == apiOpenAI && len(rp.calls) > 0:
		return "tool_calls"
	case api == apiOpenAI:
		return "stop"
	case len(rp.calls) > 0:
		return "tool_use"
	default:
		return "end_turn"
	}
}

func words(s string) []string {
	var out []string
	start := 0
	for i := 1; i < len(s); i++ {
		if s[i] == ' ' && s[i-1] != ' ' {
			out = append(out, s[start:i])
			start = i
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func pieces(s string, size int) []string {
	var out []string
	for len(s) > size {
		cut := size
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			_, cut = utf8.DecodeRuneInString(s)
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
