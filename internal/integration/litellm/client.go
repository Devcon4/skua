package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider manages per-run LiteLLM virtual keys and reads model metadata.
type Provider interface {
	// Mint creates the key r.Key under r.Alias. A retry with the same alias and
	// value succeeds; an alias held by another value returns ErrAliasConflict.
	Mint(ctx context.Context, r MintRequest) error
	// Revoke deletes the key with alias. A missing alias succeeds; a key owned
	// by another LiteLLM user returns ErrNotOwner.
	Revoke(ctx context.Context, alias string) error
	// ModelInfo reports whether model is a known model group and whether it is priced.
	ModelInfo(ctx context.Context, model string) (ModelInfo, error)
}

// MintRequest describes one run key. Duration is sent in whole seconds,
// rounded up, and omitted when zero.
type MintRequest struct {
	Key       string
	Alias     string
	Models    []string
	MaxBudget float64
	Duration  time.Duration
	Metadata  map[string]string
}

var (
	ErrAliasConflict = errors.New("litellm: key alias is held by another key")
	ErrNotOwner      = errors.New("litellm: key belongs to another owner")
)

// APIError is a non-2xx LiteLLM response that has no more specific error.
type APIError struct {
	Path    string
	Status  int
	Type    string
	Param   string
	Message string
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("litellm")
	if e.Path != "" {
		b.WriteString(" " + e.Path)
	}
	fmt.Fprintf(&b, ": HTTP %d", e.Status)
	if e.Type != "" {
		b.WriteString(" " + e.Type)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// Validate reports a request LiteLLM would refuse or that would mint an
// unrestricted key: a value without the sk- prefix or under 16 characters,
// an empty alias, or no models.
func (r MintRequest) Validate() error {
	switch {
	case !strings.HasPrefix(r.Key, "sk-") || len(r.Key) < 16:
		return errors.New("litellm: key value must start with sk- and be at least 16 characters")
	case r.Alias == "":
		return errors.New("litellm: key alias is empty")
	case len(r.Models) == 0:
		return errors.New("litellm: key has no models")
	case r.Duration < 0:
		return errors.New("litellm: key duration is negative")
	}
	return nil
}

const maxResponseBytes = 1 << 20

// Client is the Provider for one LiteLLM proxy, plus ModelGroups for callers that read the
// whole model list.
type Client struct {
	baseURL string
	key     string
	hc      *http.Client
}

// New returns a Client for the LiteLLM proxy at baseURL, authenticating with key as a
// bearer token. A nil hc uses http.DefaultClient.
func New(baseURL, key string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), key: key, hc: hc}
}

type generateRequest struct {
	Key       string            `json:"key"`
	KeyAlias  string            `json:"key_alias"`
	KeyType   string            `json:"key_type"`
	Models    []string          `json:"models"`
	MaxBudget float64           `json:"max_budget"`
	Duration  string            `json:"duration,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func (c *Client) Mint(ctx context.Context, r MintRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	req := generateRequest{
		Key:       r.Key,
		KeyAlias:  r.Alias,
		KeyType:   "llm_api",
		Models:    r.Models,
		MaxBudget: r.MaxBudget,
		Metadata:  r.Metadata,
	}
	if r.Duration > 0 {
		req.Duration = fmt.Sprintf("%ds", (r.Duration+time.Second-1)/time.Second)
	}
	status, body, err := c.do(ctx, http.MethodPost, "/key/generate", nil, req, maxResponseBytes)
	if err != nil {
		return err
	}
	if status/100 == 2 {
		var resp struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("litellm /key/generate: decode response: %w", err)
		}
		if resp.Key != r.Key {
			return errors.New("litellm /key/generate: response key differs from the requested value")
		}
		return nil
	}
	apiErr := parseError("/key/generate", status, body)
	if status == http.StatusBadRequest && apiErr.Param == "key_alias" {
		return c.confirm(ctx, r)
	}
	return apiErr
}

func (c *Client) confirm(ctx context.Context, r MintRequest) error {
	status, body, err := c.do(ctx, http.MethodGet, "/key/info", url.Values{"key": {r.Key}}, nil, maxResponseBytes)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		var resp struct {
			Info struct {
				KeyAlias string `json:"key_alias"`
			} `json:"info"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("litellm /key/info: decode response: %w", err)
		}
		if resp.Info.KeyAlias != r.Alias {
			return fmt.Errorf("%w: %s", ErrAliasConflict, r.Alias)
		}
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrAliasConflict, r.Alias)
	}
	return parseError("/key/info", status, body)
}

func (c *Client) Revoke(ctx context.Context, alias string) error {
	if alias == "" {
		return errors.New("litellm: key alias is empty")
	}
	status, body, err := c.do(ctx, http.MethodPost, "/key/delete", nil, map[string][]string{"key_aliases": {alias}}, maxResponseBytes)
	if err != nil {
		return err
	}
	if status/100 == 2 {
		return nil
	}
	apiErr := parseError("/key/delete", status, body)
	switch {
	case status == http.StatusNotFound && strings.Contains(apiErr.Message, "No keys found"):
		return nil
	case status == http.StatusForbidden:
		return fmt.Errorf("%w: %s: %s", ErrNotOwner, alias, apiErr.Message)
	}
	return apiErr
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in any, limit int64) (int, []byte, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reqBody io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, nil, fmt.Errorf("litellm %s %s: encode request: %w", method, path, err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("litellm %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// url.Error repeats the URL, and /key/info carries the key value in its query.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return 0, nil, fmt.Errorf("litellm %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return 0, nil, fmt.Errorf("litellm %s %s: read response: %w", method, path, err)
	}
	return resp.StatusCode, body, nil
}

func parseError(path string, status int, body []byte) *APIError {
	e := &APIError{Path: path, Status: status}
	var env struct {
		Error  json.RawMessage `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &env) != nil {
		e.Message = truncate(strings.TrimSpace(string(body)))
		return e
	}
	raw := env.Error
	if len(raw) == 0 {
		raw = env.Detail
	}
	var obj struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   any    `json:"param"`
		Error   any    `json:"error"`
	}
	var text string
	switch {
	case json.Unmarshal(raw, &obj) == nil:
		e.Message, e.Type = obj.Message, obj.Type
		if s, ok := obj.Error.(string); ok && e.Message == "" {
			e.Message = s
		}
		if p, ok := obj.Param.(string); ok && p != "None" {
			e.Param = p
		}
	case json.Unmarshal(raw, &text) == nil:
		e.Message = text
	default:
		e.Message = truncate(strings.TrimSpace(string(body)))
	}
	return e
}

func truncate(s string) string {
	const limit = 512
	if len(s) > limit {
		return s[:limit]
	}
	return s
}
