package omprpctest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
)

func (r *run) llm(args []string) error {
	if len(args) != 2 {
		return errors.New("llm needs <model> <count>")
	}
	count, err := strconv.Atoi(args[1])
	if err != nil {
		return err
	}
	for range count {
		if r.ctx.Err() != nil {
			return nil
		}
		if err := r.complete(args[0]); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) llmLoop(args []string) error {
	interval, err := durArg(args, 0)
	if err != nil {
		return err
	}
	st := r.state()
	model := st.ModelID
	if st.Provider != "" {
		model = st.Provider + "/" + st.ModelID
	}
	if st.ModelID == "" {
		return errors.New("llm-loop: no session model")
	}
	for r.ctx.Err() == nil {
		if err := r.complete(model); err != nil {
			return err
		}
		r.wait(interval)
	}
	return nil
}

func (r *run) complete(model string) error {
	mf, err := loadModels()
	if err != nil {
		return err
	}
	if mf == nil {
		return errors.New("models.yml not found in " + agentDir())
	}
	p, id, ok := resolveModel(mf, model)
	if !ok {
		return fmt.Errorf("Model not found: %s", model) //nolint:staticcheck // ST1005: mirrors omp's error text
	}
	key := p.APIKey
	if v := os.Getenv(key); v != "" {
		key = v
	}
	prompt := r.state().Script
	var (
		url     string
		body    map[string]any
		headers = map[string]string{"Content-Type": "application/json"}
	)
	switch p.API {
	case "anthropic-messages":
		url = strings.TrimSuffix(p.BaseURL, "/") + "/v1/messages"
		body = map[string]any{"model": id, "max_tokens": 256, "messages": []any{map[string]any{"role": "user", "content": scriptPrefix + prompt}}}
		headers["x-api-key"] = key
		headers["anthropic-version"] = "2023-06-01"
	default:
		url = strings.TrimSuffix(p.BaseURL, "/") + "/chat/completions"
		body = map[string]any{"model": id, "stream": false, "messages": []any{map[string]any{"role": "user", "content": scriptPrefix + prompt}}}
		headers["Authorization"] = "Bearer " + key
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if r.ctx.Err() != nil {
			return nil
		}
		return &promptError{Message: "Connection error: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return gatewayError(resp.StatusCode, raw)
	}
	return r.text(replyText(raw))
}

func resolveModel(mf *modelsFile, model string) (provider, string, bool) {
	if prov, id, ok := strings.Cut(model, "/"); ok {
		if p, ok := mf.Providers[prov]; ok && p.has(id) {
			return p, id, true
		}
	}
	names := slices.Sorted(maps.Keys(mf.Providers))
	for _, name := range names {
		if p := mf.Providers[name]; p.has(model) {
			return p, model, true
		}
	}
	for _, name := range names {
		if p := mf.Providers[name]; p.API == "openai-completions" {
			return p, model, true
		}
	}
	return provider{}, "", false
}

func gatewayError(status int, raw []byte) error {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Param   any    `json:"param"`
		} `json:"error"`
	}
	msg := fmt.Sprintf("%d %s", status, strings.TrimSpace(string(raw)))
	if json.Unmarshal(raw, &body) == nil && body.Error.Type != "" {
		param := "None"
		if body.Error.Param != nil {
			param = fmt.Sprint(body.Error.Param)
		}
		msg = fmt.Sprintf("%d %s (type=%s param=%s)", status, body.Error.Message, body.Error.Type, param)
	}
	return &promptError{HTTPStatus: status, Message: msg}
}

func replyText(raw []byte) string {
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return string(raw)
	}
	if len(body.Choices) > 0 {
		return body.Choices[0].Message.Content
	}
	var sb strings.Builder
	for _, c := range body.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
