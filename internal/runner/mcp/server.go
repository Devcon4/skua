// Package mcp is the skua stdio MCP server that exposes submit_result to the agent and
// hands validated results to the runner over a unix socket.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/version"
)

const (
	toolName = "submit_result"

	maxMessageBytes = 64 << 20
	schemaURL       = "skua://result.json"
	acceptedText    = "Result accepted by skua. Finish your turn now."
	instructions    = "Call submit_result exactly once with your final answer when the task is done. If it returns validation errors, fix them and call it again."
	toolDescription = "Submit the run's final result to skua. The arguments must match the run's result schema; errors are returned so you can correct and resubmit."
)

var defaultResultSchema = map[string]any{"type": "string", "description": "The final report as text"}

type server struct {
	inputSchema json.RawMessage
	validator   *jsonschema.Schema
	wrapped     bool
	sock        string
}

// Serve speaks MCP as newline-delimited JSON-RPC 2.0 on in and out, closing both when it
// returns. It returns nil when in ends, ctx.Err() when ctx is done, and the transport error
// when the client sends a frame that is not JSON-RPC. It reads the run schema from
// SKUA_RESULT_SCHEMA_FILE and hands valid submit_result arguments to the runner socket at
// SKUA_RUNNER_SOCK. A missing or invalid schema file is returned as an error before
// anything is read.
func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser, getenv func(string) string) error {
	s, err := newServer(getenv)
	if err != nil {
		return err
	}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "skua", Version: version.Version}, &mcpsdk.ServerOptions{
		Instructions: instructions,
		Capabilities: &mcpsdk.ServerCapabilities{Tools: &mcpsdk.ToolCapabilities{}},
	})
	srv.AddTool(&mcpsdk.Tool{Name: toolName, Description: toolDescription, InputSchema: s.inputSchema}, s.call)
	return srv.Run(ctx, &mcpsdk.IOTransport{Reader: in, Writer: out, MaxLineLength: maxMessageBytes})
}

func newServer(getenv func(string) string) (*server, error) {
	schema, wrapped, err := loadSchema(getenv(sandbox.EnvResultSchemaFile))
	if err != nil {
		return nil, fmt.Errorf("result schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(schemaURL, schema); err != nil {
		return nil, fmt.Errorf("result schema: %w", err)
	}
	v, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("result schema: %w", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("result schema: %w", err)
	}
	return &server{inputSchema: raw, validator: v, wrapped: wrapped, sock: getenv(sandbox.EnvRunnerSock)}, nil
}

func loadSchema(file string) (map[string]any, bool, error) {
	if file == "" {
		return wrapSchema(defaultResultSchema), true, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, false, err
	}
	if obj, ok := doc.(map[string]any); ok && obj["type"] == "object" {
		return obj, false, nil
	}
	return wrapSchema(doc), true, nil
}

func wrapSchema(s any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"result": s},
		"required":   []any{"result"},
	}
}

func (s *server) call(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	args := req.Params.Arguments
	if len(bytes.TrimSpace(args)) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err == nil {
		err = s.validator.Validate(inst)
	}
	if err != nil {
		return toolError("submit_result rejected: the arguments do not match the result schema.\n" + err.Error() + "\nFix the arguments and call submit_result again."), nil
	}
	value := args
	if s.wrapped {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(args, &obj); err != nil {
			return toolError("submit_result failed: " + err.Error()), nil
		}
		value = obj["result"]
	}
	if s.sock == "" {
		return toolError("submit_result failed: no runner socket configured"), nil
	}
	if err := Submit(s.sock, value); err != nil {
		return toolError("submit_result failed: " + err.Error()), nil
	}
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: acceptedText}}}, nil
}

func toolError(text string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}, IsError: true}
}
