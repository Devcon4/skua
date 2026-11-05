package wire

import (
	"encoding/json"
	"testing"
	"time"
)

func TestJSONNames(t *testing.T) {
	expires := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"profile", Profile{Name: "p", Description: "d", Models: []string{"m"}, Ready: true},
			`{"name":"p","description":"d","models":["m"],"ready":true}`},
		{"create run", CreateRunRequest{Profile: "p", ClientKey: "k", Workspace: &Workspace{Repo: "o/r", BaseRef: "main"}, Task: Task{Prompt: "go", Schema: json.RawMessage(`{"type":"object"}`)}},
			`{"profile":"p","clientKey":"k","workspace":{"repo":"o/r","baseRef":"main"},"task":{"prompt":"go","schema":{"type":"object"}}}`},
		{"create run without workspace", CreateRunRequest{Profile: "p", ClientKey: "k", Task: Task{Prompt: "go"}},
			`{"profile":"p","clientKey":"k","task":{"prompt":"go"}}`},
		{"empty outputs", Outputs{}, `{}`},
		{"tool start", ToolStartData{ID: "t1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)},
			`{"id":"t1","name":"bash","args":{"cmd":"ls"}}`},
		{"tool update", ToolUpdateData{ID: "t1", Partial: json.RawMessage(`"out"`)}, `{"id":"t1","partial":"out"}`},
		{"tool end", ToolEndData{ID: "t1", Result: json.RawMessage(`"ok"`), IsError: true}, `{"id":"t1","result":"ok","isError":true}`},
		{"status", StatusData{Phase: "Running"}, `{"phase":"Running"}`},
		{"end", EndData{Phase: "Failed", Reason: "Cancelled", Result: json.RawMessage(`null`)},
			`{"phase":"Failed","reason":"Cancelled","result":null,"outputs":{}}`},
		{"log event", LogEvent{ID: 3, Event: Event{Type: EventText, Data: json.RawMessage(`{"delta":"hi"}`)}},
			`{"id":3,"type":"text","data":{"delta":"hi"}}`},
		{"runner events", RunnerEventsRequest{Run: "r", Events: []RunnerEvent{{Seq: 1, Type: EventThinking, Data: json.RawMessage(`{"delta":"x"}`)}}},
			`{"run":"r","events":[{"seq":1,"type":"thinking","data":{"delta":"x"}}]}`},
		{"ack", Ack{Ack: 9}, `{"ack":9}`},
		{"runner result", RunnerResult{Run: "r", Phase: "Succeeded", Reason: "Completed", Result: json.RawMessage(`{"a":1}`), Outputs: Outputs{PullRequest: 4}},
			`{"run":"r","phase":"Succeeded","reason":"Completed","result":{"a":1},"outputs":{"pullRequest":4}}`},
		{"assign", Assign{Run: "r", Task: Task{Prompt: "go"}, Gateway: Gateway{BaseURL: "http://gw", Key: "k", Models: []string{"m"}}, Forge: &Forge{Token: "t", ExpiresAt: expires}},
			`{"run":"r","task":{"prompt":"go"},"gateway":{"baseUrl":"http://gw","key":"k","models":["m"]},"forge":{"token":"t","expiresAt":"2026-11-03T13:00:00Z"}}`},
		{"steer", Steer{Message: "faster"}, `{"message":"faster"}`},
		{"abort", Abort{}, `{}`},
		{"token", Token{Token: "t", ExpiresAt: expires}, `{"token":"t","expiresAt":"2026-11-03T13:00:00Z"}`},
		{"command", Command{ID: 2, Type: CommandAbort, Data: json.RawMessage(`{}`)}, `{"id":2,"type":"abort","data":{}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.v)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.want {
				t.Fatalf("got  %s\nwant %s", b, c.want)
			}
		})
	}
}
