package wire

import (
	"encoding/json"
	"time"
)

const (
	HeaderRunnerVersion = "X-Skua-Runner-Version"
	HeartbeatInterval   = 15 * time.Second
	// MaxEventsBodyBytes is the largest POST /v1alpha1/runner/events body skua-api accepts.
	MaxEventsBodyBytes = 1 << 20
)

// RunnerEvent is an Event as posted by the runner; Seq only deduplicates and acknowledges.
type RunnerEvent struct {
	Seq  int64           `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type RunnerEventsRequest struct {
	Run    string        `json:"run"`
	Events []RunnerEvent `json:"events"`
}

type Ack struct {
	Ack int64 `json:"ack"`
}

type RunnerResult struct {
	Run     string          `json:"run"`
	Phase   string          `json:"phase"`
	Reason  string          `json:"reason"`
	Result  json.RawMessage `json:"result"`
	Outputs Outputs         `json:"outputs"`
}
