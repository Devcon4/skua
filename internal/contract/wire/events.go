package wire

import (
	"encoding/json"
)

const (
	EventText       = "text"
	EventThinking   = "thinking"
	EventToolStart  = "tool_start"
	EventToolUpdate = "tool_update"
	EventToolEnd    = "tool_end"
	EventStatus     = "status"
	EventEnd        = "end"
)

type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type TextData struct {
	Delta string `json:"delta"`
}

type ThinkingData struct {
	Delta string `json:"delta"`
}

type ToolStartData struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type ToolUpdateData struct {
	ID      string          `json:"id"`
	Partial json.RawMessage `json:"partial"`
}

type ToolEndData struct {
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	IsError bool            `json:"isError"`
}

type StatusData struct {
	Phase  string `json:"phase"`
	Reason string `json:"reason,omitempty"`
}

type EndData struct {
	Phase   string          `json:"phase"`
	Reason  string          `json:"reason"`
	Result  json.RawMessage `json:"result"`
	Outputs Outputs         `json:"outputs"`
}

// LogEvent is an Event in skua-api's per-run log. IDs increase within the log and across skua-api restarts.
type LogEvent struct {
	ID int64 `json:"id"`
	Event
}
