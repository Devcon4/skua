package omprpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/integration/litellm"
	"github.com/Devcon4/skua/internal/runner/agent"
)

const (
	providerOpenAI    = "skua-gateway"
	providerAnthropic = "skua-gateway-anthropic"
	agentDirEnv       = "PI_CODING_AGENT_DIR"
	// omp's default of 10 retries keeps a turn silent for about 14 minutes against a dead upstream.
	maxRetries = 2
)

type modelsFile struct {
	Providers map[string]*modelsProvider `json:"providers"`
}

type modelsProvider struct {
	BaseURL string        `json:"baseUrl"`
	API     string        `json:"api"`
	Auth    string        `json:"auth,omitempty"`
	APIKey  string        `json:"apiKey"`
	Models  []modelsEntry `json:"models"`
}

type modelsEntry struct {
	ID            string `json:"id"`
	ContextWindow int64  `json:"contextWindow,omitempty"`
}

type configFile struct {
	EnabledModels []string          `json:"enabledModels"`
	ModelRoles    map[string]string `json:"modelRoles"`
	Tools         map[string]bool   `json:"tools"`
	Retry         retryConfig       `json:"retry"`
}

type retryConfig struct {
	MaxRetries int `json:"maxRetries"`
}

type mcpFile struct {
	MCPServers map[string]mcpServer `json:"mcpServers"`
}

type mcpServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func writeConfig(ctx context.Context, hc *http.Client, p agent.StartParams) ([]string, error) {
	if len(p.Models) == 0 {
		return nil, &agent.Error{Reason: v1alpha1.ReasonModelNotFound, Message: "profile lists no models"}
	}
	base := strings.TrimSuffix(p.GatewayBaseURL, "/")
	groups, err := litellm.New(base, p.GatewayKey, hc).ModelGroups(ctx)
	if err != nil {
		return nil, &agent.Error{Reason: v1alpha1.ReasonGatewayError, Message: err.Error()}
	}
	models := modelsFile{Providers: map[string]*modelsProvider{}}
	enabled := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		i := slices.IndexFunc(groups, func(g litellm.ModelGroup) bool { return g.Name == m })
		if i < 0 {
			return nil, &agent.Error{Reason: v1alpha1.ReasonModelNotFound, Message: fmt.Sprintf("gateway has no model group %q", m)}
		}
		name, prov := providerOpenAI, &modelsProvider{BaseURL: base + "/v1", API: "openai-completions", APIKey: sandbox.EnvGatewayKey}
		if slices.Contains(groups[i].Providers, "anthropic") {
			name, prov = providerAnthropic, &modelsProvider{BaseURL: base, API: "anthropic-messages", Auth: "apiKey", APIKey: sandbox.EnvGatewayKey}
		}
		if existing, ok := models.Providers[name]; ok {
			prov = existing
		}
		prov.Models = append(prov.Models, modelsEntry{ID: m, ContextWindow: groups[i].MaxInputTokens})
		models.Providers[name] = prov
		enabled = append(enabled, name+"/"+m)
	}
	helper := enabled[0]
	if len(enabled) > 1 {
		helper = enabled[1]
	}
	cfg := configFile{
		EnabledModels: enabled,
		ModelRoles:    map[string]string{"default": enabled[0], "smol": helper, "tiny": helper},
		Tools:         map[string]bool{"xdev": false},
		Retry:         retryConfig{MaxRetries: maxRetries},
	}
	if err := os.MkdirAll(p.AgentDir, 0o700); err != nil {
		return nil, startFailed(err)
	}
	if err := writeYAML(filepath.Join(p.AgentDir, "models.yml"), models); err != nil {
		return nil, startFailed(err)
	}
	if err := writeYAML(filepath.Join(p.AgentDir, "config.yml"), cfg); err != nil {
		return nil, startFailed(err)
	}
	if len(p.MCP.Command) > 0 {
		mcp := mcpFile{MCPServers: map[string]mcpServer{"skua": {
			Type:    "stdio",
			Command: p.MCP.Command[0],
			Args:    append([]string{}, p.MCP.Command[1:]...),
			Env:     map[string]string{sandbox.EnvRunnerSock: p.MCP.SocketPath, sandbox.EnvResultSchemaFile: p.MCP.SchemaFile},
		}}}
		b, err := json.MarshalIndent(mcp, "", "  ")
		if err != nil {
			return nil, startFailed(err)
		}
		if err := os.WriteFile(filepath.Join(p.AgentDir, "mcp.json"), b, 0o600); err != nil {
			return nil, startFailed(err)
		}
	}
	return enabled, nil
}

func writeYAML(path string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
