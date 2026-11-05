package wire

import (
	"encoding/json"
	"time"
)

type Profile struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Models      []string `json:"models"`
	Ready       bool     `json:"ready"`
}

type CreateRunRequest struct {
	Profile   string     `json:"profile"`
	ClientKey string     `json:"clientKey"`
	Workspace *Workspace `json:"workspace,omitempty"`
	Task      Task       `json:"task"`
}

type Workspace struct {
	Repo    string `json:"repo"`
	BaseRef string `json:"baseRef"`
}

type Task struct {
	Prompt string          `json:"prompt"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// Run is the dev-facing view of an AgentRun. Result holds a JSON string or any other JSON value.
type Run struct {
	Name      string          `json:"name"`
	Profile   string          `json:"profile"`
	ClientKey string          `json:"clientKey"`
	Phase     string          `json:"phase"`
	Reason    string          `json:"reason"`
	Result    json.RawMessage `json:"result"`
	Outputs   Outputs         `json:"outputs"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Outputs struct {
	Branch      string `json:"branch,omitempty"`
	PullRequest int64  `json:"pullRequest,omitempty"`
}
