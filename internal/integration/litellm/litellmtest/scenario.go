package litellmtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"sigs.k8s.io/yaml"
)

// Scenario scripts the replies to every request whose user messages contain Match.
// Turn i answers a request that already holds i assistant messages.
type Scenario struct {
	Name  string `json:"name"`
	Match string `json:"match"`
	Turns []Turn `json:"turns"`
}

// Turn is one scripted assistant reply. A turn with neither Text nor ToolCalls replies "ok".
// A nil Usage means prompt tokens = request bytes / 4 and completion tokens = reply bytes / 4.
type Turn struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	Usage     *Usage     `json:"usage,omitempty"`
	Fault     *Fault     `json:"fault,omitempty"`
}

// ToolCall is a scripted tool call. Arguments is a JSON object, or a JSON string holding one;
// empty means {}.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Usage overrides the token counts reported for a turn.
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
}

// Fault disturbs the first Times attempts of a turn (every attempt when Times is 0), counted
// per scenario and turn. FirstByteDelay holds the response on the server clock, Status replies
// with that HTTP error, and DropAfterChunks closes the connection after that many stream chunks.
type Fault struct {
	Status          int
	Times           int
	FirstByteDelay  time.Duration
	DropAfterChunks int
}

type faultJSON struct {
	Status          int    `json:"status,omitempty"`
	Times           int    `json:"times,omitempty"`
	FirstByteDelay  string `json:"firstByteDelay,omitempty"`
	DropAfterChunks int    `json:"dropAfterChunks,omitempty"`
}

func (f Fault) MarshalJSON() ([]byte, error) {
	j := faultJSON{Status: f.Status, Times: f.Times, DropAfterChunks: f.DropAfterChunks}
	if f.FirstByteDelay > 0 {
		j.FirstByteDelay = f.FirstByteDelay.String()
	}
	return json.Marshal(j)
}

func (f *Fault) UnmarshalJSON(data []byte) error {
	var j faultJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return err
	}
	*f = Fault{Status: j.Status, Times: j.Times, DropAfterChunks: j.DropAfterChunks}
	if j.FirstByteDelay != "" {
		d, err := time.ParseDuration(j.FirstByteDelay)
		if err != nil {
			return fmt.Errorf("firstByteDelay: %w", err)
		}
		f.FirstByteDelay = d
	}
	return nil
}

// ParseScenario decodes one YAML scenario, rejecting unknown fields and invalid values.
func ParseScenario(data []byte) (Scenario, error) {
	var s Scenario
	if err := yaml.UnmarshalStrict(data, &s); err != nil {
		return Scenario{}, err
	}
	if err := s.validate(); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

// LoadFS parses every file in fsys matching pattern, in file name order, which is also the
// order in which scenarios are matched. Scenario names must be unique.
func LoadFS(fsys fs.FS, pattern string) ([]Scenario, error) {
	names, err := fs.Glob(fsys, pattern)
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	out := make([]Scenario, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		s, err := ParseScenario(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if prev, ok := seen[s.Name]; ok {
			return nil, fmt.Errorf("%s: scenario %q already defined in %s", name, s.Name, prev)
		}
		seen[s.Name] = name
		out = append(out, s)
	}
	return out, nil
}

func (s Scenario) validate() error {
	if s.Name == "" {
		return errors.New("name is required")
	}
	if s.Match == "" {
		return fmt.Errorf("scenario %q: match is required", s.Name)
	}
	for i, t := range s.Turns {
		if err := t.validate(); err != nil {
			return fmt.Errorf("scenario %q turn %d: %w", s.Name, i, err)
		}
	}
	return nil
}

func (t Turn) validate() error {
	for i, c := range t.ToolCalls {
		if c.Name == "" {
			return fmt.Errorf("toolCalls[%d]: name is required", i)
		}
		if _, err := c.argumentsJSON(); err != nil {
			return fmt.Errorf("toolCalls[%d]: %w", i, err)
		}
	}
	if u := t.Usage; u != nil && (u.PromptTokens < 0 || u.CompletionTokens < 0) {
		return errors.New("usage: token counts must not be negative")
	}
	if f := t.Fault; f != nil {
		switch {
		case f.Status != 0 && (f.Status < 400 || f.Status > 599):
			return fmt.Errorf("fault: status %d is not 4xx or 5xx", f.Status)
		case f.Times < 0 || f.DropAfterChunks < 0 || f.FirstByteDelay < 0:
			return errors.New("fault: values must not be negative")
		case f.Status == 0 && f.DropAfterChunks == 0 && f.FirstByteDelay == 0:
			return errors.New("fault: set status, firstByteDelay or dropAfterChunks")
		}
	}
	return nil
}

func (c ToolCall) argumentsJSON() (string, error) {
	raw := bytes.TrimSpace(c.Arguments)
	if len(raw) == 0 || string(raw) == "null" {
		return "{}", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		raw = []byte(s)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	return buf.String(), nil
}
