package omprpc

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Devcon4/skua/api/v1alpha1"
)

func readYAML(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestStartModelUnknownToGateway(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary", "no-such-model"})
	requireReason(t, h.start(), v1alpha1.ReasonModelNotFound)
}

func TestConfigFiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary", "e2e-claude", "e2e-helper"})
	h.mustStart()

	var cfg configFile
	readYAML(t, filepath.Join(h.params.AgentDir, "config.yml"), &cfg)
	want := configFile{
		EnabledModels: []string{"skua-gateway/e2e-primary", "skua-gateway-anthropic/e2e-claude", "skua-gateway/e2e-helper"},
		ModelRoles:    map[string]string{"default": "skua-gateway/e2e-primary", "smol": "skua-gateway-anthropic/e2e-claude", "tiny": "skua-gateway-anthropic/e2e-claude"},
		Tools:         map[string]bool{"xdev": false},
		Retry:         retryConfig{MaxRetries: 2},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("config.yml = %+v, want %+v", cfg, want)
	}

	var models modelsFile
	readYAML(t, filepath.Join(h.params.AgentDir, "models.yml"), &models)
	base := h.params.GatewayBaseURL
	wantModels := modelsFile{Providers: map[string]*modelsProvider{
		"skua-gateway": {BaseURL: base + "/v1", API: "openai-completions", APIKey: "SKUA_LITELLM_KEY", Models: []modelsEntry{
			{ID: "e2e-primary", ContextWindow: 128000}, {ID: "e2e-helper", ContextWindow: 128000},
		}},
		"skua-gateway-anthropic": {BaseURL: base, API: "anthropic-messages", Auth: "apiKey", APIKey: "SKUA_LITELLM_KEY", Models: []modelsEntry{
			{ID: "e2e-claude", ContextWindow: 200000},
		}},
	}}
	if !reflect.DeepEqual(models, wantModels) {
		t.Fatalf("models.yml = %+v", models)
	}

	var mcp mcpFile
	b, err := os.ReadFile(filepath.Join(h.params.AgentDir, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &mcp); err != nil {
		t.Fatal(err)
	}
	wantMCP := mcpFile{MCPServers: map[string]mcpServer{"skua": {
		Type: "stdio", Command: "/skua/bin/skua-runner", Args: []string{"mcp"},
		Env: map[string]string{"SKUA_RUNNER_SOCK": "/skua/run/runner.sock", "SKUA_RESULT_SCHEMA_FILE": "/skua/run/schema.json"},
	}}}
	if !reflect.DeepEqual(mcp, wantMCP) {
		t.Fatalf("mcp.json = %+v", mcp)
	}

	opens := h.commands("open_session")
	if len(opens) != 1 || opens[0]["provider"] != "skua-gateway" || opens[0]["modelId"] != "e2e-primary" {
		t.Fatalf("open_session = %v", opens)
	}
	if subs := h.commands("set_subagent_subscription"); len(subs) != 1 || subs[0]["level"] != "events" {
		t.Fatalf("set_subagent_subscription = %v", subs)
	}

	h.prompt("SCRIPT:llm skua-gateway/e2e-primary 1")
	h.mu.Lock()
	defer h.mu.Unlock()
	bearer := []string{"Bearer " + testKey}
	if got := h.auth["/model_group/info"]; !reflect.DeepEqual(got, bearer) {
		t.Fatalf("model_group/info auth = %v", got)
	}
	if got := h.auth["/v1/chat/completions"]; !reflect.DeepEqual(got, bearer) {
		t.Fatalf("chat completions auth = %v", got)
	}
}

func TestConfigSingleModelRoles(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-claude"})
	enabled, err := writeConfig(context.Background(), http.DefaultClient, h.params)
	if err != nil {
		t.Fatal(err)
	}
	var cfg configFile
	readYAML(t, filepath.Join(h.params.AgentDir, "config.yml"), &cfg)
	m := "skua-gateway-anthropic/e2e-claude"
	if !reflect.DeepEqual(enabled, []string{m}) || !reflect.DeepEqual(cfg.ModelRoles, map[string]string{"default": m, "smol": m, "tiny": m}) {
		t.Fatalf("enabled = %v, config = %+v", enabled, cfg)
	}
}
