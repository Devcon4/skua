package litellmtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"
)

var epoch = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func start(t *testing.T, scenarios ...Scenario) (*httptest.Server, *testingclock.FakeClock) {
	t.Helper()
	clk := testingclock.NewFakeClock(epoch)
	srv := httptest.NewServer(NewServer(scenarios, clk))
	t.Cleanup(srv.Close)
	return srv, clk
}

func post(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func openAIRequest(stream bool, messages ...map[string]any) map[string]any {
	return map[string]any{"model": "fake-primary", "stream": stream, "messages": messages}
}

func anthropicRequest(stream bool, messages ...map[string]any) map[string]any {
	return map[string]any{"model": "claude-fake", "max_tokens": 1024, "stream": stream, "messages": messages}
}

func user(text string) map[string]any { return map[string]any{"role": "user", "content": text} }
func assistant(text string) map[string]any {
	return map[string]any{"role": "assistant", "content": text}
}

type gotCall struct {
	Name string
	Args string
}

type openAIStream struct {
	text   string
	calls  []gotCall
	finish string
	usage  bool
	chunks int
	done   bool
	err    error
}

type openAIToolCallDelta struct {
	Index    int `json:"index"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content   string                `json:"content"`
			ToolCalls []openAIToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

func readOpenAIStream(r io.Reader) openAIStream {
	var out openAIStream
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			out.done = true
			continue
		}
		out.chunks++
		var c openAIChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			out.err = err
			return out
		}
		out.add(c)
	}
	out.err = sc.Err()
	return out
}

func (out *openAIStream) add(c openAIChunk) {
	if c.Usage != nil {
		out.usage = true
	}
	for _, ch := range c.Choices {
		out.text += ch.Delta.Content
		for _, tc := range ch.Delta.ToolCalls {
			out.addToolCallDelta(tc)
		}
		if ch.FinishReason != nil {
			out.finish = *ch.FinishReason
		}
	}
}

func (out *openAIStream) addToolCallDelta(tc openAIToolCallDelta) {
	for len(out.calls) <= tc.Index {
		out.calls = append(out.calls, gotCall{})
	}
	out.calls[tc.Index].Name += tc.Function.Name
	out.calls[tc.Index].Args += tc.Function.Arguments
}

type anthropicStream struct {
	events []string
	text   string
	calls  []gotCall
	stop   string
	err    error
}

func readAnthropicStream(r io.Reader) anthropicStream {
	var out anthropicStream
	blocks := map[int]string{}
	sc := bufio.NewScanner(r)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if e, ok := strings.CutPrefix(line, "event: "); ok {
			event = e
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			out.err = err
			return out
		}
		if ev.Type != event {
			out.err = errors.New("event line " + event + " does not match data type " + ev.Type)
			return out
		}
		out.events = append(out.events, ev.Type)
		switch ev.Type {
		case "content_block_start":
			blocks[ev.Index] = ev.ContentBlock.Type
			if ev.ContentBlock.Type == "tool_use" {
				out.calls = append(out.calls, gotCall{Name: ev.ContentBlock.Name})
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				out.text += ev.Delta.Text
			case "input_json_delta":
				out.calls[len(out.calls)-1].Args += ev.Delta.PartialJSON
			}
		case "message_delta":
			out.stop = ev.Delta.StopReason
		}
	}
	out.err = sc.Err()
	return out
}

func requestLog(t *testing.T, srv *httptest.Server) []RequestRecord {
	t.Helper()
	resp, err := http.Get(srv.URL + "/_test/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out []RequestRecord
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("arguments %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("arguments = %s, want %s", got, want)
	}
}

const toolScenario = `
name: tools
match: TOOLS-PLEASE
turns:
  - text: "Calling the tool now."
    toolCalls:
      - name: mcp__skua_submit_result
        arguments: {title: "A title", summary: "Ünïcode summary text", confidence: 0.75}
`

func TestOpenAIStreamTextAndToolCall(t *testing.T) {
	srv, _ := start(t, parse(t, toolScenario))
	resp := post(t, srv.URL+"/v1/chat/completions", openAIRequest(true, user("please TOOLS-PLEASE")))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	got := readOpenAIStream(resp.Body)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.text != "Calling the tool now." {
		t.Fatalf("text = %q", got.text)
	}
	if len(got.calls) != 1 || got.calls[0].Name != "mcp__skua_submit_result" {
		t.Fatalf("calls = %+v", got.calls)
	}
	sameJSON(t, got.calls[0].Args, `{"title":"A title","summary":"Ünïcode summary text","confidence":0.75}`)
	if got.finish != "tool_calls" || !got.done || !got.usage {
		t.Fatalf("finish %q done %v usage %v", got.finish, got.done, got.usage)
	}
	if got.chunks < 6 {
		t.Fatalf("chunks = %d, want text and arguments split over several chunks", got.chunks)
	}
}

func TestOpenAIStreamUsageChunkWhenRequested(t *testing.T) {
	srv, _ := start(t)
	req := openAIRequest(true, user("anything"))
	req["stream_options"] = map[string]any{"include_usage": true}
	resp := post(t, srv.URL+"/v1/chat/completions", req)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"choices":[]`) || !strings.Contains(string(body), `"prompt_tokens"`) {
		t.Fatalf("no separate usage chunk in %s", body)
	}
}

func TestAnthropicStreamTextAndToolUse(t *testing.T) {
	srv, _ := start(t, parse(t, toolScenario))
	resp := post(t, srv.URL+"/v1/messages", anthropicRequest(true, map[string]any{
		"role":    "user",
		"content": []map[string]any{{"type": "text", "text": "please TOOLS-PLEASE"}},
	}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := readAnthropicStream(resp.Body)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.text != "Calling the tool now." {
		t.Fatalf("text = %q", got.text)
	}
	if len(got.calls) != 1 || got.calls[0].Name != "mcp__skua_submit_result" {
		t.Fatalf("calls = %+v", got.calls)
	}
	sameJSON(t, got.calls[0].Args, `{"title":"A title","summary":"Ünïcode summary text","confidence":0.75}`)
	if got.stop != "tool_use" {
		t.Fatalf("stop_reason = %q", got.stop)
	}
	n := len(got.events)
	if n < 4 || got.events[0] != "message_start" || got.events[n-2] != "message_delta" || got.events[n-1] != "message_stop" {
		t.Fatalf("events = %v", got.events)
	}
	for _, want := range []string{"content_block_start", "content_block_delta", "content_block_stop"} {
		if !strings.Contains(strings.Join(got.events, ","), want) {
			t.Fatalf("events %v lack %s", got.events, want)
		}
	}
}

func TestNonStreamingReplies(t *testing.T) {
	srv, _ := start(t, parse(t, toolScenario))

	resp := post(t, srv.URL+"/v1/chat/completions", openAIRequest(false, user("TOOLS-PLEASE")))
	var oa struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&oa); err != nil {
		t.Fatal(err)
	}
	m := oa.Choices[0].Message
	if m.Content != "Calling the tool now." || len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Name != "mcp__skua_submit_result" || m.ToolCalls[0].ID == "" {
		t.Fatalf("openai message = %+v", m)
	}
	if oa.Choices[0].FinishReason != "tool_calls" || oa.Usage.PromptTokens == 0 || oa.Usage.CompletionTokens == 0 {
		t.Fatalf("finish %q usage %+v", oa.Choices[0].FinishReason, oa.Usage)
	}

	resp = post(t, srv.URL+"/v1/messages", anthropicRequest(false, user("TOOLS-PLEASE")))
	var an struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&an); err != nil {
		t.Fatal(err)
	}
	if len(an.Content) != 2 || an.Content[0].Text != "Calling the tool now." || an.Content[1].Type != "tool_use" || an.StopReason != "tool_use" {
		t.Fatalf("anthropic message = %+v", an)
	}
	sameJSON(t, string(an.Content[1].Input), `{"title":"A title","summary":"Ünïcode summary text","confidence":0.75}`)
}

func openAIText(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Choices[0].Message.Content
}

func TestTurnFollowsAssistantMessageCount(t *testing.T) {
	srv, _ := start(t, parse(t, `
name: turns
match: TURNS
turns:
  - text: first
  - text: second
`))
	url := srv.URL + "/v1/chat/completions"
	cases := []struct {
		messages []map[string]any
		want     string
	}{
		{[]map[string]any{user("TURNS")}, "first"},
		{[]map[string]any{user("TURNS")}, "first"},
		{[]map[string]any{user("TURNS"), assistant("first"), user("go on")}, "second"},
		{[]map[string]any{user("TURNS"), assistant("first"), user("go on"), assistant("second"), user("more")}, "ok"},
	}
	for i, c := range cases {
		if got := openAIText(t, post(t, url, openAIRequest(false, c.messages...))); got != c.want {
			t.Fatalf("case %d: text = %q, want %q", i, got, c.want)
		}
	}
	turns := []int{}
	for _, r := range requestLog(t, srv) {
		turns = append(turns, r.Turn)
	}
	if !reflect.DeepEqual(turns, []int{0, 0, 1, 2}) {
		t.Fatalf("logged turns = %v", turns)
	}
}

func TestFirstMatchingScenarioWins(t *testing.T) {
	srv, _ := start(t,
		parse(t, "name: a\nmatch: SHARED\nturns: [{text: from-a}]\n"),
		parse(t, "name: b\nmatch: SHARED\nturns: [{text: from-b}]\n"),
	)
	if got := openAIText(t, post(t, srv.URL+"/v1/chat/completions", openAIRequest(false, user("x SHARED y")))); got != "from-a" {
		t.Fatalf("text = %q", got)
	}
}

func TestStatusFaultFailsThenSucceeds(t *testing.T) {
	srv, _ := start(t, parse(t, `
name: retry
match: RETRY
turns:
  - text: after retry
    fault: {status: 429, times: 1}
`))
	url := srv.URL + "/v1/chat/completions"
	resp := post(t, url, openAIRequest(false, user("RETRY")))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first attempt status %d", resp.StatusCode)
	}
	var e struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	if e.Error.Type != "rate_limit_error" || e.Error.Code != "429" {
		t.Fatalf("error = %+v", e.Error)
	}
	for i := range 2 {
		if got := openAIText(t, post(t, url, openAIRequest(false, user("RETRY")))); got != "after retry" {
			t.Fatalf("retry %d: text = %q", i, got)
		}
	}
	log := requestLog(t, srv)
	if len(log) != 3 || log[0].Fault == nil || log[0].Fault.Status != 429 || log[0].Status != 429 || log[1].Fault != nil || log[2].Fault != nil {
		t.Fatalf("log = %+v", log)
	}
}

func TestResetRearmsFaultsAndClearsLog(t *testing.T) {
	srv, _ := start(t, parse(t, `
name: retry
match: RETRY
turns:
  - text: after retry
    fault: {status: 429, times: 1}
`))
	url := srv.URL + "/v1/chat/completions"
	for _, want := range []int{http.StatusTooManyRequests, http.StatusOK} {
		if resp := post(t, url, openAIRequest(false, user("RETRY"))); resp.StatusCode != want {
			t.Fatalf("before reset: status %d, want %d", resp.StatusCode, want)
		}
	}
	resp := post(t, srv.URL+"/_test/reset", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset status %d", resp.StatusCode)
	}
	if log := requestLog(t, srv); len(log) != 0 {
		t.Fatalf("log after reset = %+v", log)
	}
	if resp := post(t, url, openAIRequest(false, user("RETRY"))); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after reset: status %d, want 429", resp.StatusCode)
	}
	if log := requestLog(t, srv); len(log) != 1 || log[0].Status != http.StatusTooManyRequests {
		t.Fatalf("log = %+v", log)
	}
}

func TestAnthropicStatusFaultShape(t *testing.T) {
	srv, _ := start(t, parse(t, "name: down\nmatch: DOWN\nturns: [{fault: {status: 503}}]\n"))
	for range 2 {
		resp := post(t, srv.URL+"/v1/messages", anthropicRequest(true, user("DOWN")))
		var e struct {
			Type  string `json:"type"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable || e.Type != "error" || e.Error.Type != "overloaded_error" {
			t.Fatalf("status %d error %+v", resp.StatusCode, e)
		}
	}
}

func TestFirstByteDelayWaitsOnClock(t *testing.T) {
	srv, clk := start(t, parse(t, `
name: slow
match: SLOW
turns:
  - text: late
    fault: {firstByteDelay: 30s}
`))
	done := make(chan *http.Response, 1)
	data, _ := json.Marshal(openAIRequest(true, user("SLOW")))
	go func() {
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(data))
		if err != nil {
			t.Error(err)
			close(done)
			return
		}
		done <- resp
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !clk.HasWaiters() {
		if time.Now().After(deadline) {
			t.Fatal("server never waited on the clock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	clk.Step(29 * time.Second)
	select {
	case <-done:
		t.Fatal("response arrived before the delay elapsed")
	case <-time.After(100 * time.Millisecond):
	}
	clk.Step(time.Second)
	select {
	case resp := <-done:
		if resp == nil {
			t.FailNow()
		}
		defer func() { _ = resp.Body.Close() }()
		if got := readOpenAIStream(resp.Body); got.text != "late" || !got.done {
			t.Fatalf("stream = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no response after the delay elapsed")
	}
}

func TestDropAfterChunksEndsStreamEarly(t *testing.T) {
	srv, _ := start(t, parse(t, `
name: drop
match: DROP
turns:
  - text: one two three four five six
    fault: {dropAfterChunks: 2}
`))
	resp := post(t, srv.URL+"/v1/chat/completions", openAIRequest(true, user("DROP")))
	got := readOpenAIStream(resp.Body)
	if got.chunks != 2 || got.done || got.finish != "" || got.err == nil {
		t.Fatalf("chunks %d done %v finish %q err %v", got.chunks, got.done, got.finish, got.err)
	}

	resp = post(t, srv.URL+"/v1/messages", anthropicRequest(true, user("DROP")))
	an := readAnthropicStream(resp.Body)
	if len(an.events) != 2 || an.err == nil {
		t.Fatalf("events %v err %v", an.events, an.err)
	}
}

func TestUnmatchedRequestGetsOK(t *testing.T) {
	srv, _ := start(t, parse(t, "name: other\nmatch: NEVER-SENT\nturns: [{text: nope}]\n"))
	if got := openAIText(t, post(t, srv.URL+"/v1/chat/completions", openAIRequest(false, user("hello")))); got != "ok" {
		t.Fatalf("openai text = %q", got)
	}
	got := readAnthropicStream(post(t, srv.URL+"/v1/messages", anthropicRequest(true, user("hello"))).Body)
	if got.err != nil || got.text != "ok" || got.stop != "end_turn" {
		t.Fatalf("anthropic stream = %+v", got)
	}
}

func TestRequestLogListsEveryRequest(t *testing.T) {
	srv, clk := start(t, parse(t, toolScenario))
	post(t, srv.URL+"/v1/chat/completions", openAIRequest(false, user("TOOLS-PLEASE")))
	clk.Step(time.Minute)
	post(t, srv.URL+"/v1/messages", anthropicRequest(true, user("TOOLS-PLEASE"), assistant("x"), user("y")))
	post(t, srv.URL+"/v1/chat/completions", openAIRequest(true, user("unmatched")))
	want := []RequestRecord{
		{Time: epoch, API: "openai", Model: "fake-primary", Stream: false, Scenario: "tools", Turn: 0, Status: 200},
		{Time: epoch.Add(time.Minute), API: "anthropic", Model: "claude-fake", Stream: true, Scenario: "tools", Turn: 1, Status: 200},
		{Time: epoch.Add(time.Minute), API: "openai", Model: "fake-primary", Stream: true, Scenario: "", Turn: 0, Status: 200},
	}
	got := requestLog(t, srv)
	if len(got) != len(want) {
		t.Fatalf("log = %+v", got)
	}
	for i := range want {
		if !got[i].Time.Equal(want[i].Time) {
			t.Fatalf("record %d time = %v, want %v", i, got[i].Time, want[i].Time)
		}
		got[i].Time = want[i].Time
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestModelEndpoints(t *testing.T) {
	srv, _ := start(t)
	info := func(query string) []map[string]any {
		resp, err := http.Get(srv.URL + "/model_group/info" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.Data == nil {
			t.Fatalf("data is null for %q", query)
		}
		return out.Data
	}
	if got := info(""); len(got) != 4 {
		t.Fatalf("groups = %v", got)
	}
	claude := info("?model_group=e2e-claude")
	if len(claude) != 1 || !reflect.DeepEqual(claude[0]["providers"], []any{"anthropic"}) || claude[0]["input_cost_per_token"].(float64) <= 0 {
		t.Fatalf("e2e-claude = %v", claude)
	}
	if got := info("?model_group=unknown"); len(got) != 0 {
		t.Fatalf("unknown = %v", got)
	}

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range models.Data {
		ids[m.ID] = true
	}
	if !ids["e2e-primary"] || !ids["fake-primary"] || !ids["claude-fake"] {
		t.Fatalf("models = %v", ids)
	}
}
