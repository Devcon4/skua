package omprpctest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
)

const testMCPEnv = "FAKEOMP_TEST_MCP"

func TestMain(m *testing.M) {
	if os.Getenv(testMCPEnv) == "1" {
		os.Exit(echoMCPServer())
	}
	// Race-built children otherwise sleep 1 s at exit.
	_ = os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	os.Exit(m.Run())
}

func echoMCPServer() int {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "echo", Version: "0"}, nil)
	srv.AddTool(&mcpsdk.Tool{Name: "submit_result", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			text := req.Params.Name + " accepted " + string(req.Params.Arguments)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}}, nil
		})
	if err := srv.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type frame map[string]any

func (f frame) str(path ...string) string {
	var cur any = map[string]any(f)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[p]
	}
	switch v := cur.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func (f frame) typ() string { return f.str("type") }

type peer struct {
	t       *testing.T
	in      *io.PipeWriter
	frames  chan frame
	code    chan int
	mu      sync.Mutex
	maxLine int
	ids     int
}

func start(t *testing.T, args ...string) *peer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &peer{t: t, in: inW, frames: make(chan frame, 100000), code: make(chan int, 1)}
	go func() {
		p.code <- Main(append([]string{"--mode", "rpc"}, args...), inR, outW)
		_ = outW.Close()
	}()
	go p.read(outR)
	t.Cleanup(func() { _ = inW.Close() })
	return p
}

func (p *peer) read(r io.Reader) {
	defer close(p.frames)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 70<<20)
	var chunks bytes.Buffer
	for sc.Scan() {
		line := sc.Bytes()
		p.mu.Lock()
		p.maxLine = max(p.maxLine, len(line)+1)
		p.mu.Unlock()
		var f frame
		if json.Unmarshal(line, &f) != nil {
			continue
		}
		if f.typ() == "rpc_chunk" {
			seg, _ := base64.StdEncoding.DecodeString(f.str("data"))
			chunks.Write(seg)
			if f["index"].(float64)+1 < f["count"].(float64) {
				continue
			}
			f = nil
			_ = json.Unmarshal(chunks.Bytes(), &f)
			chunks.Reset()
		}
		p.frames <- f
	}
}

func (p *peer) send(cmd map[string]any) string {
	p.t.Helper()
	p.ids++
	id := fmt.Sprintf("t%d", p.ids)
	cmd["id"] = id
	b, _ := json.Marshal(cmd)
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		p.t.Fatalf("write command: %v", err)
	}
	return id
}

func (p *peer) waitFor(timeout time.Duration, pred func(frame) bool) (frame, []frame) {
	p.t.Helper()
	var seen []frame
	deadline := time.After(timeout)
	for {
		select {
		case f, ok := <-p.frames:
			if !ok {
				p.t.Fatalf("stdout closed; frames seen: %v", seen)
			}
			seen = append(seen, f)
			if pred(f) {
				return f, seen
			}
		case <-deadline:
			p.t.Fatalf("timed out; frames seen: %v", seen)
		}
	}
}

func (p *peer) call(cmd map[string]any) frame {
	p.t.Helper()
	id := p.send(cmd)
	f, _ := p.waitFor(10*time.Second, func(f frame) bool { return f.typ() == "response" && f.str("id") == id })
	return f
}

func (p *peer) prompt(msg string) []frame {
	p.t.Helper()
	id := p.send(map[string]any{"type": "prompt", "message": msg})
	_, seen := p.waitFor(30*time.Second, func(f frame) bool { return f.typ() == "prompt_result" && f.str("id") == id })
	return seen
}

func (p *peer) close() int {
	p.t.Helper()
	_ = p.in.Close()
	select {
	case c := <-p.code:
		return c
	case <-time.After(10 * time.Second):
		p.t.Fatal("Main did not return after stdin closed")
		return -1
	}
}

func last(frames []frame) frame { return frames[len(frames)-1] }

func textDeltas(frames []frame) []string {
	var out []string
	for _, f := range frames {
		if f.typ() == "message_update" && f.str("assistantMessageEvent", "type") == "text_delta" {
			out = append(out, f.str("assistantMessageEvent", "delta"))
		}
	}
	return out
}

func writeAgentFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNegotiateProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, versions, want string
		ok                   bool
	}{
		{name: "default offers 2", versions: "1,2", want: "[1,2]", ok: true},
		{name: "only 1 refuses 2", versions: "1", want: "[1]", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := start(t, "--protocol-versions", tc.versions)
			ready, _ := p.waitFor(5*time.Second, func(f frame) bool { return true })
			if ready.typ() != "ready" || ready.str("supportedProtocolVersions") != tc.want {
				t.Fatalf("ready = %v", ready)
			}
			resp := p.call(map[string]any{"type": "negotiate_protocol", "protocolVersion": 2})
			if got := resp["success"] == true; got != tc.ok {
				t.Fatalf("negotiate success = %v, want %v: %v", got, tc.ok, resp)
			}
			if !tc.ok && resp.str("error") != "Unsupported RPC protocol version: 2" {
				t.Fatalf("error = %q", resp.str("error"))
			}
		})
	}
}

func TestExitBeforeReady(t *testing.T) {
	var out bytes.Buffer
	if code := Main([]string{"--mode", "rpc", "--exit-before-ready"}, strings.NewReader(""), &out); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestReplyYieldsTextDeltas(t *testing.T) {
	p := start(t)
	frames := p.prompt("SCRIPT:reply hi")
	if got := strings.Join(textDeltas(frames), ""); got != "hi" {
		t.Fatalf("text = %q", got)
	}
	res := last(frames)
	if res.str("status") != "completed" || res["sessionSettled"] != true {
		t.Fatalf("prompt_result = %v", res)
	}
	if got := p.call(map[string]any{"type": "get_last_assistant_text"}).str("data", "text"); got != "hi" {
		t.Fatalf("last text = %q", got)
	}
	if code := p.close(); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
}

func TestScriptOnALaterLine(t *testing.T) {
	for name, tc := range map[string]struct{ prompt, want string }{
		"whole prompt":         {"SCRIPT:reply hi", "hi"},
		"after a preamble":     {"Complete assignment thoroughly:\n\n  SCRIPT:reply hi\n", "hi"},
		"no script line":       {"Complete assignment thoroughly:\n\ndo it", "ok"},
		"prefix mid-line only": {"see SCRIPT:reply hi", "ok"},
	} {
		t.Run(name, func(t *testing.T) {
			p := start(t)
			frames := p.prompt(tc.prompt)
			if got := strings.Join(textDeltas(frames), ""); got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
			if res := last(frames); res.str("status") != "completed" {
				t.Fatalf("prompt_result = %v", res)
			}
		})
	}
}

func TestSteerDeliveredAtNextToolEnd(t *testing.T) {
	p := start(t)
	gate := filepath.Join(t.TempDir(), "gate")
	id := p.send(map[string]any{"type": "prompt", "message": "SCRIPT:steps 3 10ms " + gate})
	_, before := p.waitFor(10*time.Second, func(f frame) bool { return f.typ() == "tool_execution_start" })
	steerID := p.send(map[string]any{"type": "steer", "message": "S1"})
	resp, during := p.waitFor(10*time.Second, func(f frame) bool { return f.typ() == "response" && f.str("id") == steerID })
	if resp["success"] != true {
		t.Fatalf("steer = %v", resp)
	}
	writeAgentFile(t, filepath.Dir(gate), "gate", "")
	_, after := p.waitFor(30*time.Second, func(f frame) bool { return f.typ() == "prompt_result" && f.str("id") == id })
	frames := slices.Concat(before, during, after)
	if last(frames).str("status") != "completed" {
		t.Fatalf("prompt_result = %v", last(frames))
	}

	var kinds []string
	for _, f := range frames {
		switch {
		case f.typ() == "message_start" && f.str("message", "role") == "user" && strings.Contains(f.str("message", "content"), `"S1"`):
			kinds = append(kinds, "user:S1")
		case f.typ() == "queue_update":
			kinds = append(kinds, "queue:"+f.str("steering"))
		case f.typ() == "tool_execution_start", f.typ() == "tool_execution_end":
			kinds = append(kinds, f.typ())
		}
	}
	want := []string{"tool_execution_start", `queue:["S1"]`, "tool_execution_end", "user:S1", "queue:[]", "tool_execution_start", "tool_execution_end", "tool_execution_start", "tool_execution_end"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("order = %v\nwant %v", kinds, want)
	}
	want = []string{"steer-received: S1", "step 1", "step 2", "step 3"}
	if got := textDeltas(frames); !slices.Equal(got, want) {
		t.Fatalf("texts = %v, want %v", got, want)
	}
}

func TestOpenSessionResumes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	open := func() frame {
		p := start(t)
		resp := p.call(map[string]any{"type": "open_session", "sessionDir": dir})
		if resp["success"] != true {
			t.Fatalf("open_session = %v", resp)
		}
		if code := p.close(); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
		return resp
	}
	first, second := open(), open()
	if first.str("data", "resumed") != "false" || second.str("data", "resumed") != "true" {
		t.Fatalf("resumed: first %v, second %v", first, second)
	}
	if first.str("data", "sessionId") != second.str("data", "sessionId") {
		t.Fatalf("session ids differ: %v %v", first, second)
	}
}

func TestOpenSessionUnknownModel(t *testing.T) {
	agent := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agent)
	writeAgentFile(t, agent, "models.yml", "providers:\n  skua-gateway:\n    baseUrl: http://127.0.0.1:1/v1\n    apiKey: SKUA_LITELLM_KEY\n    api: openai-completions\n    models:\n      - id: e2e-primary\n")
	p := start(t)
	dir := filepath.Join(t.TempDir(), "session")
	resp := p.call(map[string]any{"type": "open_session", "sessionDir": dir, "provider": "skua-gateway", "modelId": "nope"})
	if resp["success"] != false || resp.str("error") != "Model not found: skua-gateway/nope" {
		t.Fatalf("open_session = %v", resp)
	}
	resp = p.call(map[string]any{"type": "open_session", "sessionDir": dir, "provider": "skua-gateway", "modelId": "e2e-primary"})
	if resp["success"] != true {
		t.Fatalf("open_session = %v", resp)
	}
}

func TestScriptErrorIsOmpShaped(t *testing.T) {
	p := start(t, "--settle-delay", "300ms")
	res := last(p.prompt("SCRIPT:error 422 budget_exceeded"))
	if res.str("status") != "error" || res.str("error", "httpStatus") != "422" || res.str("error", "retryable") != "false" {
		t.Fatalf("prompt_result = %v", res)
	}
	if res["sessionSettled"] != false {
		t.Fatalf("prompt_result sessionSettled = %v, want false", res["sessionSettled"])
	}
	if !regexp.MustCompile(`^422 .*\(type=budget_exceeded .*\)$`).MatchString(res.str("error", "message")) {
		t.Fatalf("message = %q", res.str("error", "message"))
	}
	if st := p.call(map[string]any{"type": "get_state"}); st.str("data", "isSettled") != "false" {
		t.Fatalf("get_state before session_settled = %v", st)
	}
	p.waitFor(10*time.Second, func(f frame) bool { return f.typ() == "session_settled" })
	if st := p.call(map[string]any{"type": "get_state"}); st.str("data", "isSettled") != "true" {
		t.Fatalf("get_state after session_settled = %v", st)
	}
}

func TestLLMAgainstFakeLLM(t *testing.T) {
	srv := httptest.NewServer(litellmtest.NewServer([]litellmtest.Scenario{
		{Name: "refused", Match: "SCRIPT:llm e2e-primary", Turns: []litellmtest.Turn{{Fault: &litellmtest.Fault{Status: http.StatusForbidden}}}},
		{Name: "served", Match: "SCRIPT:llm e2e-helper", Turns: []litellmtest.Turn{{Text: "hello"}}},
	}, clock.RealClock{}))
	t.Cleanup(srv.Close)
	agent := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agent)
	t.Setenv("SKUA_LITELLM_KEY", "sk-skua-test")
	writeAgentFile(t, agent, "models.yml", fmt.Sprintf("providers:\n  skua-gateway:\n    baseUrl: %s/v1\n    apiKey: SKUA_LITELLM_KEY\n    api: openai-completions\n    models:\n      - id: e2e-primary\n      - id: e2e-helper\n", srv.URL))

	p := start(t)
	frames := p.prompt("SCRIPT:llm e2e-helper 2")
	if res := last(frames); res.str("status") != "completed" {
		t.Fatalf("prompt_result = %v", res)
	}
	if got := textDeltas(frames); !slices.Equal(got, []string{"hello", "hello"}) {
		t.Fatalf("texts = %v", got)
	}

	res := last(p.prompt("SCRIPT:llm e2e-primary 3"))
	if res.str("status") != "error" || res.str("error", "httpStatus") != "403" || res.str("error", "retryable") != "false" {
		t.Fatalf("prompt_result = %v", res)
	}
	if !strings.HasPrefix(res.str("error", "message"), "403 ") {
		t.Fatalf("message = %q", res.str("error", "message"))
	}
}

func TestLLMUnlistedModelReachesGateway(t *testing.T) {
	srv := httptest.NewServer(litellmtest.NewServer([]litellmtest.Scenario{
		{Name: "outside-key", Match: "SCRIPT:llm e2e-other", Turns: []litellmtest.Turn{{Fault: &litellmtest.Fault{Status: http.StatusForbidden}}}},
	}, clock.RealClock{}))
	t.Cleanup(srv.Close)
	agent := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agent)
	t.Setenv("SKUA_LITELLM_KEY", "sk-skua-test")
	writeAgentFile(t, agent, "models.yml", fmt.Sprintf("providers:\n  skua-gateway-anthropic:\n    baseUrl: http://127.0.0.1:1\n    apiKey: SKUA_LITELLM_KEY\n    api: anthropic-messages\n    auth: apiKey\n    models:\n      - id: claude-e2e\n  skua-gateway:\n    baseUrl: %s/v1\n    apiKey: SKUA_LITELLM_KEY\n    api: openai-completions\n    models:\n      - id: e2e-primary\n", srv.URL))

	p := start(t)
	res := last(p.prompt("SCRIPT:llm e2e-other 1"))
	if res.str("status") != "error" || res.str("error", "httpStatus") != "403" {
		t.Fatalf("prompt_result = %v", res)
	}
	resp, err := http.Get(srv.URL + "/_test/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var log []litellmtest.RequestRecord
	if err := json.NewDecoder(resp.Body).Decode(&log); err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].Model != "e2e-other" || log[0].Status != http.StatusForbidden {
		t.Fatalf("requests = %+v", log)
	}
}

func TestOversizeFramesAreChunked(t *testing.T) {
	p := start(t)
	if resp := p.call(map[string]any{"type": "negotiate_protocol", "protocolVersion": 2}); resp["success"] != true {
		t.Fatalf("negotiate = %v", resp)
	}
	const n = 1500000
	frames := p.prompt(fmt.Sprintf("SCRIPT:reply-bytes %d", n))
	var end frame
	for _, f := range frames {
		if f.typ() == "message_end" && f.str("message", "role") == "assistant" {
			end = f
		}
	}
	if end == nil {
		t.Fatal("no assistant message_end")
	}
	if got := p.call(map[string]any{"type": "get_last_assistant_text"}).str("data", "text"); len(got) != n {
		t.Fatalf("last text length = %d", len(got))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maxLine > maxFrameBytes {
		t.Fatalf("physical frame of %d bytes", p.maxLine)
	}
}

func TestAbortStopsSleep(t *testing.T) {
	p := start(t)
	id := p.send(map[string]any{"type": "prompt", "message": "SCRIPT:sleep 60"})
	p.waitFor(10*time.Second, func(f frame) bool { return f.typ() == "tool_execution_start" })
	started := time.Now()
	abortID := p.send(map[string]any{"type": "abort"})
	resp, seen := p.waitFor(10*time.Second, func(f frame) bool { return f.typ() == "response" && f.str("id") == abortID })
	if resp["success"] != true {
		t.Fatalf("abort = %v", resp)
	}
	i := slices.IndexFunc(seen, func(f frame) bool { return f.typ() == "prompt_result" && f.str("id") == id })
	if i < 0 || seen[i].str("status") != "aborted" || time.Since(started) > 5*time.Second {
		t.Fatalf("frames = %v after %v", seen, time.Since(started))
	}
}

func TestSubmitCallsMCPServer(t *testing.T) {
	agent := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agent)
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"skua": map[string]any{
		"type": "stdio", "command": os.Args[0], "env": map[string]string{testMCPEnv: "1"},
	}}})
	writeAgentFile(t, agent, "mcp.json", string(cfg))
	p := start(t)
	frames := p.prompt(`SCRIPT:submit {"answer":42}`)
	if res := last(frames); res.str("status") != "completed" {
		t.Fatalf("prompt_result = %v", res)
	}
	var startF, endF frame
	for _, f := range frames {
		switch f.typ() {
		case "tool_execution_start":
			startF = f
		case "tool_execution_end":
			endF = f
		}
	}
	if startF.str("toolName") != "mcp__skua_submit_result" || startF.str("args") != `{"answer":42}` {
		t.Fatalf("tool start = %v", startF)
	}
	if endF.str("isError") != "false" || !strings.Contains(endF.str("result"), `submit_result accepted {\"answer\":42}`) {
		t.Fatalf("tool end = %v", endF)
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitWorkspace(t *testing.T) (origin, work string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "fakeomp test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@skua.invalid")
	}
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	work = filepath.Join(root, "work")
	gitT(t, root, "init", "-q", "--bare", "-b", "main", origin)
	gitT(t, root, "clone", "-q", origin, work)
	gitT(t, work, "commit", "-q", "--allow-empty", "-m", "init")
	gitT(t, work, "push", "-q", "origin", "HEAD:main")
	t.Chdir(work)
	t.Setenv("SKUA_RUN", "fake-r1")
	return origin, work
}

func TestPushAndPR(t *testing.T) {
	origin, _ := gitWorkspace(t)
	var got struct {
		auth, path string
		body       map[string]string
	}
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth, got.path = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number":7,"html_url":"http://forge/o/r/pull/7","head":{"ref":"skua/fake-r1"}}`)
	}))
	t.Cleanup(forge.Close)
	tokenFile := filepath.Join(t.TempDir(), "forge-token.json")
	writeAgentFile(t, filepath.Dir(tokenFile), filepath.Base(tokenFile), `{"token":"ghs_test","expires_at":"2099-01-01T00:00:00Z"}`)
	t.Setenv("SKUA_FORGE_TOKEN_FILE", tokenFile)
	t.Setenv("SKUA_FORGE_API_URL", forge.URL)
	t.Setenv("SKUA_REPO", "o/r")

	p := start(t)
	frames := p.prompt("SCRIPT:push-and-pr notes.txt")
	if res := last(frames); res.str("status") != "completed" {
		t.Fatalf("prompt_result = %v", res)
	}
	if texts := textDeltas(frames); !slices.Contains(texts, "pr: 7") {
		t.Fatalf("texts = %v", texts)
	}
	if files := gitT(t, origin, "ls-tree", "--name-only", "skua/fake-r1"); files != "notes.txt" {
		t.Fatalf("branch files = %q", files)
	}
	if got.auth != "Bearer ghs_test" || got.path != "/repos/o/r/pulls" || got.body["head"] != "skua/fake-r1" || got.body["base"] != "main" {
		t.Fatalf("pull request call = %+v", got)
	}
}

func TestPushMainReportsExitCode(t *testing.T) {
	origin, _ := gitWorkspace(t)
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mainBefore := gitT(t, origin, "rev-parse", "main")
	p := start(t)
	frames := p.prompt("SCRIPT:push-main")
	if texts := textDeltas(frames); !slices.Contains(texts, "push-main: 1") {
		t.Fatalf("texts = %v", texts)
	}
	if after := gitT(t, origin, "rev-parse", "main"); after != mainBefore {
		t.Fatalf("main moved: %s -> %s", mainBefore, after)
	}
}
