package litellmtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (s *server) plan(c conversation) reply {
	turn, userTexts := conversationTurn(c)
	scenario := s.matchScenario(userTexts)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	rp := reply{seq: s.seq}
	rp.record = RequestRecord{Time: s.clock.Now(), API: c.api, Model: c.model, Stream: c.stream, Turn: turn, Status: http.StatusOK}
	t := s.scriptedTurn(&rp, scenario, turn)

	rp.text = t.Text
	if t.Text == "" && len(t.ToolCalls) == 0 {
		rp.text = "ok"
	}
	replyBytes := len(rp.text) + planToolCalls(&rp, c.api, t, turn)
	rp.usage = replyUsage(t, c.size, replyBytes)
	s.log = append(s.log, rp.record)
	return rp
}

func conversationTurn(c conversation) (turn int, userTexts []string) {
	for _, m := range c.messages {
		switch m.Role {
		case "assistant":
			turn++
		case "user":
			userTexts = append(userTexts, textOf(m.Content))
		}
	}
	return turn, userTexts
}

func (s *server) matchScenario(userTexts []string) int {
	for i, sc := range s.scenarios {
		if sc.Match != "" && containsAny(userTexts, sc.Match) {
			return i
		}
	}
	return -1
}

func (s *server) scriptedTurn(rp *reply, scenario, turn int) Turn {
	if scenario < 0 {
		return Turn{}
	}
	sc := s.scenarios[scenario]
	rp.record.Scenario = sc.Name
	var t Turn
	if turn < len(sc.Turns) {
		t = sc.Turns[turn]
	}
	s.injectFault(rp, turnKey{scenario, turn}, t.Fault)
	return t
}

func (s *server) injectFault(rp *reply, k turnKey, f *Fault) {
	if f == nil {
		return
	}
	s.attempts[k]++
	faultExhausted := f.Times != 0 && s.attempts[k] > f.Times
	if faultExhausted {
		return
	}
	applied := *f
	rp.fault = &applied
	rp.record.Fault = &applied
	if f.Status != 0 {
		rp.record.Status = f.Status
	}
}

func planToolCalls(rp *reply, api string, t Turn, turn int) (callBytes int) {
	prefix := "call_"
	if api == apiAnthropic {
		prefix = "toolu_"
	}
	for i, tc := range t.ToolCalls {
		args, err := tc.argumentsJSON()
		if err != nil {
			rp.err = fmt.Errorf("scenario %q turn %d tool call %d: %w", rp.record.Scenario, turn, i, err)
			rp.record.Status = http.StatusInternalServerError
			return callBytes
		}
		rp.calls = append(rp.calls, toolCall{id: fmt.Sprintf("%s%d_%d", prefix, rp.seq, i), name: tc.Name, args: args})
		callBytes += len(tc.Name) + len(args)
	}
	return callBytes
}

func replyUsage(t Turn, promptBytes, replyBytes int) Usage {
	if t.Usage != nil {
		return *t.Usage
	}
	return Usage{PromptTokens: promptBytes / 4, CompletionTokens: replyBytes / 4}
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
