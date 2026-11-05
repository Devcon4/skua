package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

const t4Schema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string", "minLength": 3},
    "summary": {"type": "string"},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1}
  },
  "required": ["title", "summary", "confidence"],
  "additionalProperties": false
}`

type testServer struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *io.PipeReader
	done   chan error
	subs   chan json.RawMessage
	reject string
}

func startServer(t *testing.T, schema, reject string) *testServer {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "runner.sock")
	env := map[string]string{sandbox.EnvRunnerSock: sock}
	if schema != "" {
		f := filepath.Join(dir, "schema.json")
		if err := os.WriteFile(f, []byte(schema), 0o600); err != nil {
			t.Fatal(err)
		}
		env[sandbox.EnvResultSchemaFile] = f
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{t: t, done: make(chan error, 1), subs: make(chan json.RawMessage, 10), reject: reject}
	go func() {
		for {
			sub, err := ln.Accept()
			if err != nil {
				return
			}
			if s.reject != "" {
				_ = sub.Reject(s.reject)
				continue
			}
			s.subs <- sub.Value
			_ = sub.Ack()
		}
	}()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { s.done <- Serve(ctx, inR, outW, func(k string) string { return env[k] }) }()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		_ = outR.Close()
		_ = ln.Close()
	})
	s.in, s.out = inW, outR
	return s
}

func (s *testServer) connect(version string) *mcpsdk.ClientSession {
	s.t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "omp-coding-agent", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), &mcpsdk.IOTransport{Reader: s.out, Writer: s.in}, &mcpsdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (s *testServer) rawCall(line string) map[string]any {
	s.t.Helper()
	if _, err := s.in.Write([]byte(line + "\n")); err != nil {
		s.t.Fatal(err)
	}
	resp, err := bufio.NewReader(s.out).ReadBytes('\n')
	if err != nil {
		s.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(resp, &m); err != nil {
		s.t.Fatal(err)
	}
	return m
}

func (s *testServer) serveErr() error {
	s.t.Helper()
	select {
	case err := <-s.done:
		return err
	case <-time.After(5 * time.Second):
		s.t.Fatal("Serve did not return")
		return nil
	}
}

func submit(t *testing.T, cs *mcpsdk.ClientSession, args string) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "submit_result", Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcpsdk.TextContent).Text, res.IsError
}

func inputSchema(t *testing.T, cs *mcpsdk.ClientSession) any {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "submit_result" {
		t.Fatalf("tools %+v", res.Tools)
	}
	return res.Tools[0].InputSchema
}

func (s *testServer) received() json.RawMessage {
	s.t.Helper()
	select {
	case v := <-s.subs:
		return v
	case <-time.After(5 * time.Second):
		s.t.Fatal("no submission reached the listener")
		return nil
	}
}

func (s *testServer) assertNothingReceived() {
	s.t.Helper()
	select {
	case v := <-s.subs:
		s.t.Fatalf("unexpected submission %s", v)
	default:
	}
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestInitializeThenToolsListReturnsRunSchema(t *testing.T) {
	cs := startServer(t, t4Schema, "").connect("2025-11-25")
	res := cs.InitializeResult()
	if res.ProtocolVersion != "2025-11-25" || res.ServerInfo.Name != "skua" {
		t.Fatalf("initialize result %+v", res)
	}
	if !strings.Contains(res.Instructions, "submit_result") {
		t.Fatalf("instructions %q", res.Instructions)
	}
	if got, want := inputSchema(t, cs), decode(t, t4Schema); !reflect.DeepEqual(got, want) {
		t.Fatalf("inputSchema %v, want %v", got, want)
	}
}

func TestLatestSDKClientConnects(t *testing.T) {
	s := startServer(t, "", "")
	cs := s.connect("")
	if v := cs.InitializeResult().ProtocolVersion; !slices.Contains(mcpsdk.SupportedProtocolVersions(), v) {
		t.Fatalf("negotiated %q", v)
	}
	if text, isErr := submit(t, cs, `{"result":"text"}`); isErr {
		t.Fatalf("got error %q", text)
	}
	if got := s.received(); string(got) != `"text"` {
		t.Fatalf("listener got %s", got)
	}
}

func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	for _, tc := range []struct{ requested, want string }{
		{"2025-06-18", "2025-06-18"},
		{"2024-11-05", "2024-11-05"},
		{"2099-01-01", "2025-11-25"},
	} {
		t.Run(tc.requested, func(t *testing.T) {
			s := startServer(t, "", "")
			resp := s.rawCall(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + tc.requested + `","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`)
			result, _ := resp["result"].(map[string]any)
			if result["protocolVersion"] != tc.want {
				t.Fatalf("initialize response %v, want protocolVersion %s", resp, tc.want)
			}
		})
	}
}

func TestMalformedFrameEndsSession(t *testing.T) {
	s := startServer(t, "", "")
	if _, err := s.in.Write([]byte("{bad\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.serveErr(); err == nil {
		t.Fatal("Serve returned nil after a malformed frame")
	}
	if rest, err := io.ReadAll(s.out); err != nil || len(rest) != 0 {
		t.Fatalf("server wrote %q (err %v) after a malformed frame", rest, err)
	}
}

func TestInvalidArgsReturnErrorAndNothingReachesSocket(t *testing.T) {
	s := startServer(t, t4Schema, "")
	text, isErr := submit(t, s.connect(""), `{"title":"Capital of France","summary":"Paris","confidence":7}`)
	if !isErr {
		t.Fatalf("want tool error, got %q", text)
	}
	if !strings.Contains(text, "confidence") {
		t.Fatalf("error does not name confidence: %q", text)
	}
	s.assertNothingReceived()
}

func TestValidCallReachesListenerAndIsAccepted(t *testing.T) {
	s := startServer(t, t4Schema, "")
	text, isErr := submit(t, s.connect(""), `{"title":"Capital of France","summary":"Paris","confidence":0.95}`)
	if isErr || !strings.Contains(text, "Result accepted by skua") {
		t.Fatalf("got %q isError=%v", text, isErr)
	}
	got := s.received()
	if want := decode(t, `{"title":"Capital of France","summary":"Paris","confidence":0.95}`); !reflect.DeepEqual(decode(t, string(got)), want) {
		t.Fatalf("listener got %s", got)
	}
}

func TestArraySchemaIsWrappedAndUnwrapped(t *testing.T) {
	s := startServer(t, `{"type":"array","items":{"type":"string"}}`, "")
	cs := s.connect("")
	want := decode(t, `{"type":"object","properties":{"result":{"type":"array","items":{"type":"string"}}},"required":["result"]}`)
	if got := inputSchema(t, cs); !reflect.DeepEqual(got, want) {
		t.Fatalf("inputSchema %v, want %v", got, want)
	}
	if text, isErr := submit(t, cs, `{"result":[1]}`); !isErr {
		t.Fatalf("want tool error, got %q", text)
	}
	s.assertNothingReceived()
	if text, isErr := submit(t, cs, `{"result":["a","b"]}`); isErr {
		t.Fatalf("got error %q", text)
	}
	if got := s.received(); string(got) != `["a","b"]` {
		t.Fatalf("listener got %s, want unwrapped array", got)
	}
}

func TestNoSchemaAcceptsResultText(t *testing.T) {
	s := startServer(t, "", "")
	cs := s.connect("")
	want := decode(t, `{"type":"object","properties":{"result":{"type":"string","description":"The final report as text"}},"required":["result"]}`)
	if got := inputSchema(t, cs); !reflect.DeepEqual(got, want) {
		t.Fatalf("inputSchema %v, want %v", got, want)
	}
	if text, isErr := submit(t, cs, `{"result":"text"}`); isErr || !strings.Contains(text, "Result accepted by skua") {
		t.Fatalf("got %q isError=%v", text, isErr)
	}
	if got := s.received(); string(got) != `"text"` {
		t.Fatalf("listener got %s", got)
	}
	if text, isErr := submit(t, cs, `{}`); !isErr {
		t.Fatalf("want tool error for missing result, got %q", text)
	}
}

func TestRunnerRejectionIsReturnedToAgent(t *testing.T) {
	s := startServer(t, "", "result is over the cap")
	text, isErr := submit(t, s.connect(""), `{"result":"text"}`)
	if !isErr || !strings.Contains(text, "result is over the cap") {
		t.Fatalf("got %q isError=%v", text, isErr)
	}
}

func TestServeReturnsNilWhenInputEnds(t *testing.T) {
	s := startServer(t, "", "")
	_ = s.in.Close()
	if err := s.serveErr(); err != nil {
		t.Fatalf("Serve returned %v", err)
	}
}

func TestSubmitWithoutListenerFails(t *testing.T) {
	if err := Submit(filepath.Join(t.TempDir(), "none.sock"), json.RawMessage(`"x"`)); err == nil {
		t.Fatal("want error")
	}
}

func TestListenerSkipsInvalidJSON(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "runner.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan json.RawMessage, 1)
	go func() {
		s, err := ln.Accept()
		if err != nil {
			return
		}
		got <- s.Value
		_ = s.Ack()
	}()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("{bad\n")); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	_ = conn.Close()
	if err != nil || !strings.Contains(reply, `"ok":false`) {
		t.Fatalf("reply %q err %v", reply, err)
	}
	if err := Submit(sock, json.RawMessage(`{bad`)); err == nil {
		t.Fatal("want error for invalid JSON")
	}
	if err := Submit(sock, json.RawMessage("{\n  \"a\": 1\n}")); err != nil {
		t.Fatal(err)
	}
	if v := <-got; string(v) != `{"a":1}` {
		t.Fatalf("got %s", v)
	}
}
