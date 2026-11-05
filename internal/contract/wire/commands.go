package wire

import (
	"encoding/json"
	"time"
)

const (
	CommandAssign = "assign"
	CommandSteer  = "steer"
	CommandAbort  = "abort"
	CommandToken  = "token"
)

// Command is one item of the runner command stream. IDs increase per pod, also across skua-api restarts.
type Command struct {
	ID   int64           `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type Assign struct {
	Run       string     `json:"run"`
	Workspace *Workspace `json:"workspace,omitempty"`
	Task      Task       `json:"task"`
	Gateway   Gateway    `json:"gateway"`
	Forge     *Forge     `json:"forge,omitempty"`
}

type Steer struct {
	Message string `json:"message"`
}

type Abort struct{}

type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Gateway struct {
	BaseURL string   `json:"baseUrl"`
	Key     string   `json:"key"`
	Models  []string `json:"models"`
}

type Forge struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}
