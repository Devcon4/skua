package omprpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func TestMapEvent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		frame, typ, data string
	}{
		{`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hi"}}`, wire.EventText, `{"delta":"hi"}`},
		{`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"hm"}}`, wire.EventThinking, `{"delta":"hm"}`},
		{`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"ls"}}`, wire.EventToolStart, `{"id":"c1","name":"bash","args":{"command":"ls"}}`},
		{`{"type":"tool_execution_update","toolCallId":"c1","toolName":"bash","args":{},"partialResult":{"content":[]}}`, wire.EventToolUpdate, `{"id":"c1","partial":{"content":[]}}`},
		{`{"type":"tool_stream_update","toolCallId":"c1","toolName":"edit","update":{"diff":"+a"}}`, wire.EventToolUpdate, `{"id":"c1","partial":{"diff":"+a"}}`},
		{`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[]},"isError":true}`, wire.EventToolEnd, `{"id":"c1","result":{"content":[]},"isError":true}`},
	} {
		e, ok := mapEvent([]byte(tc.frame))
		if !ok || e.Type != tc.typ || string(e.Data) != tc.data {
			t.Errorf("mapEvent(%s) = %s %s %v, want %s %s", tc.frame, e.Type, e.Data, ok, tc.typ, tc.data)
		}
	}
	for _, frame := range []string{
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start"}}`,
		`{"type":"message_end","message":{"role":"assistant"}}`,
		`{"type":"turn_end"}`,
	} {
		if e, ok := mapEvent([]byte(frame)); ok {
			t.Errorf("mapEvent(%s) relayed %v", frame, e)
		}
	}
}

func TestEventsFromFakeOmp(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"})
	h.mustStart()
	if r := h.prompt("SCRIPT:steps 1 10ms"); r.Status != "completed" {
		t.Fatalf("turn = %+v", r)
	}
	start := h.waitEvent(isType(wire.EventToolStart))
	var sd wire.ToolStartData
	if err := json.Unmarshal(start.Data, &sd); err != nil || sd.Name != "bash" || string(sd.Args) != `{"command":"echo step 1"}` || sd.ID == "" {
		t.Fatalf("tool_start = %s", start.Data)
	}
	update := h.waitEvent(isType(wire.EventToolUpdate))
	var ud wire.ToolUpdateData
	if err := json.Unmarshal(update.Data, &ud); err != nil || ud.ID != sd.ID || !strings.Contains(string(ud.Partial), "step 1 running") {
		t.Fatalf("tool_update = %s", update.Data)
	}
	end := h.waitEvent(isType(wire.EventToolEnd))
	var ed wire.ToolEndData
	if err := json.Unmarshal(end.Data, &ed); err != nil || ed.ID != sd.ID || ed.IsError || !strings.Contains(string(ed.Result), "step 1") {
		t.Fatalf("tool_end = %s", end.Data)
	}
	h.waitEvent(func(e wire.Event) bool { return e.Type == wire.EventText && string(e.Data) == `{"delta":"step 1"}` })
}
