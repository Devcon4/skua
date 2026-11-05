package litellmtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (s *server) plan(c conversation) reply {
	turn := 0
	var userTexts []string
	for _, m := range c.messages {
		switch m.Role {
		case "assistant":
			turn++
		case "user":
			userTexts = append(userTexts, textOf(m.Content))
		}
	}
	scenario := -1
	for i, sc := range s.scenarios {
		if sc.Match != "" && containsAny(userTexts, sc.Match) {
			scenario = i
			break
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	rp := reply{seq: s.seq}
	rp.record = RequestRecord{Time: s.clock.Now(), API: c.api, Model: c.model, Stream: c.stream, Turn: turn, Status: http.StatusOK}
	var t Turn
	if scenario >= 0 {
		sc := s.scenarios[scenario]
		rp.record.Scenario = sc.Name
		if turn < len(sc.Turns) {
			t = sc.Turns[turn]
		}
		if f := t.Fault; f != nil {
			k := turnKey{scenario, turn}
			s.attempts[k]++
			if f.Times == 0 || s.attempts[k] <= f.Times {
				applied := *f
				rp.fault = &applied
				rp.record.Fault = &applied
				if f.Status != 0 {
					rp.record.Status = f.Status
				}
			}
		}
	}

	rp.text = t.Text
	if t.Text == "" && len(t.ToolCalls) == 0 {
		rp.text = "ok"
	}
	replyBytes := len(rp.text)
	prefix := "call_"
	if c.api == apiAnthropic {
		prefix = "toolu_"
	}
	for i, tc := range t.ToolCalls {
		args, err := tc.argumentsJSON()
		if err != nil {
			rp.err = fmt.Errorf("scenario %q turn %d tool call %d: %w", rp.record.Scenario, turn, i, err)
			rp.record.Status = http.StatusInternalServerError
			break
		}
		rp.calls = append(rp.calls, toolCall{id: fmt.Sprintf("%s%d_%d", prefix, rp.seq, i), name: tc.Name, args: args})
		replyBytes += len(tc.Name) + len(args)
	}
	if t.Usage != nil {
		rp.usage = *t.Usage
	} else {
		rp.usage = Usage{PromptTokens: c.size / 4, CompletionTokens: replyBytes / 4}
	}
	s.log = append(s.log, rp.record)
	return rp
}

func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func containsAny(texts []string, sub string) bool {
	for _, t := range texts {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}
