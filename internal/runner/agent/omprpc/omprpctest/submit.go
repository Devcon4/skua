package omprpctest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpProtocolVersion = "2025-06-18"

type mcpServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func (r *run) submit(payload string) error {
	if !json.Valid([]byte(payload)) {
		return fmt.Errorf("submit: invalid JSON %q", payload)
	}
	args := json.RawMessage(payload)
	tc := r.startTool("mcp__skua_submit_result", args)
	text, isError, err := callSubmit(r.ctx, args)
	if err != nil {
		tc.end(err.Error(), true)
		return err
	}
	tc.end(text, isError)
	if r.ctx.Err() != nil {
		return nil
	}
	return r.text("submit_result: " + text)
}

func callSubmit(ctx context.Context, args json.RawMessage) (string, bool, error) {
	b, err := os.ReadFile(filepath.Join(agentDir(), "mcp.json"))
	if err != nil {
		return "", false, err
	}
	var cfg struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", false, fmt.Errorf("mcp.json: %w", err)
	}
	srv, ok := cfg.MCPServers["skua"]
	if !ok || srv.Command == "" {
		return "", false, errors.New("mcp.json has no skua server")
	}
	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)
	cmd.Env = os.Environ()
	for k, v := range srv.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "fakeomp", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, &mcpsdk.ClientSessionOptions{ProtocolVersion: mcpProtocolVersion})
	if err != nil {
		return "", false, fmt.Errorf("mcp initialize: %w", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "submit_result", Arguments: args})
	if err != nil {
		return "", false, fmt.Errorf("mcp tools/call: %w", err)
	}
	var texts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcpsdk.TextContent); ok {
			texts = append(texts, t.Text)
		}
	}
	return strings.Join(texts, "\n"), res.IsError, nil
}
