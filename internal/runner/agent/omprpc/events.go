package omprpc

import (
	"encoding/json"
	"sync"

	"github.com/Devcon4/skua/internal/contract/wire"
)

type ompEvent struct {
	Type                  string `json:"type"`
	AssistantMessageEvent *struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Args          json.RawMessage `json:"args"`
	PartialResult json.RawMessage `json:"partialResult"`
	Update        json.RawMessage `json:"update"`
	Result        json.RawMessage `json:"result"`
	IsError       bool            `json:"isError"`
}

func mapEvent(raw []byte) (wire.Event, bool) {
	var f ompEvent
	if json.Unmarshal(raw, &f) != nil {
		return wire.Event{}, false
	}
	switch f.Type {
	case "message_update":
		if f.AssistantMessageEvent == nil {
			return wire.Event{}, false
		}
		switch f.AssistantMessageEvent.Type {
		case "text_delta":
			return event(wire.EventText, wire.TextData{Delta: f.AssistantMessageEvent.Delta})
		case "thinking_delta":
			return event(wire.EventThinking, wire.ThinkingData{Delta: f.AssistantMessageEvent.Delta})
		}
	case "tool_execution_start":
		return event(wire.EventToolStart, wire.ToolStartData{ID: f.ToolCallID, Name: f.ToolName, Args: f.Args})
	case "tool_execution_update":
		return event(wire.EventToolUpdate, wire.ToolUpdateData{ID: f.ToolCallID, Partial: f.PartialResult})
	case "tool_stream_update":
		return event(wire.EventToolUpdate, wire.ToolUpdateData{ID: f.ToolCallID, Partial: f.Update})
	case "tool_execution_end":
		return event(wire.EventToolEnd, wire.ToolEndData{ID: f.ToolCallID, Result: f.Result, IsError: f.IsError})
	}
	return wire.Event{}, false
}

func event(typ string, data any) (wire.Event, bool) {
	b, err := json.Marshal(data)
	if err != nil {
		return wire.Event{}, false
	}
	return wire.Event{Type: typ, Data: b}, true
}

// eventQueue delivers events in order without ever blocking the frame reader.
type eventQueue struct {
	out chan wire.Event

	mu      sync.Mutex
	buf     []wire.Event
	pumping bool
}

func newEventQueue() *eventQueue {
	return &eventQueue{out: make(chan wire.Event)}
}

func (q *eventQueue) push(e wire.Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.buf = append(q.buf, e)
	if !q.pumping {
		q.pumping = true
		go q.pump()
	}
}

func (q *eventQueue) pump() {
	for {
		q.mu.Lock()
		if len(q.buf) == 0 {
			q.buf, q.pumping = nil, false
			q.mu.Unlock()
			return
		}
		e := q.buf[0]
		q.buf = q.buf[1:]
		q.mu.Unlock()
		q.out <- e
	}
}
