package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const (
	modelGroupInfoPath = "/model_group/info"
	// The unfiltered list from a large gateway exceeds the 1 MiB cap the other calls use.
	maxModelGroupsBytes = 16 << 20
)

// ModelInfo is one LiteLLM model group. Known is false when LiteLLM has no
// group by that name; Priced is true when its input cost per token is above zero.
type ModelInfo struct {
	Known     bool
	Priced    bool
	Providers []string
}

// ModelGroup is one entry of the gateway's model list. MaxInputTokens is zero when
// LiteLLM reports no context window for the group.
type ModelGroup struct {
	Name           string
	Providers      []string
	MaxInputTokens int64
}

type modelGroupEntry struct {
	ModelGroup        string   `json:"model_group"`
	Providers         []string `json:"providers"`
	InputCostPerToken *float64 `json:"input_cost_per_token"`
	MaxInputTokens    float64  `json:"max_input_tokens"`
}

func (c *Client) ModelInfo(ctx context.Context, model string) (ModelInfo, error) {
	groups, err := c.modelGroupInfo(ctx, url.Values{"model_group": {model}}, maxResponseBytes)
	if err != nil {
		return ModelInfo{}, err
	}
	for _, g := range groups {
		if g.ModelGroup == model {
			return ModelInfo{
				Known:     true,
				Priced:    g.InputCostPerToken != nil && *g.InputCostPerToken > 0,
				Providers: g.Providers,
			}, nil
		}
	}
	return ModelInfo{}, nil
}

// ModelGroups lists every model group the key can see. A non-2xx response returns an *APIError.
func (c *Client) ModelGroups(ctx context.Context) ([]ModelGroup, error) {
	entries, err := c.modelGroupInfo(ctx, nil, maxModelGroupsBytes)
	if err != nil {
		return nil, err
	}
	groups := make([]ModelGroup, len(entries))
	for i, e := range entries {
		groups[i] = ModelGroup{Name: e.ModelGroup, Providers: e.Providers, MaxInputTokens: int64(e.MaxInputTokens)}
	}
	return groups, nil
}

func (c *Client) modelGroupInfo(ctx context.Context, query url.Values, limit int64) ([]modelGroupEntry, error) {
	status, body, err := c.do(ctx, http.MethodGet, modelGroupInfoPath, query, nil, limit)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, parseError(modelGroupInfoPath, status, body)
	}
	var resp struct {
		Data []modelGroupEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("litellm %s: decode response: %w", modelGroupInfoPath, err)
	}
	return resp.Data, nil
}
