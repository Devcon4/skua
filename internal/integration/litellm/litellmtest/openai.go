package litellmtest

import (
	"fmt"
	"net/http"
)

func (s *server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model         string    `json:"model"`
		Stream        bool      `json:"stream"`
		Messages      []message `json:"messages"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	size, ok := decode(w, r, apiOpenAI, &req)
	if !ok {
		return
	}
	c := conversation{api: apiOpenAI, model: req.Model, stream: req.Stream, messages: req.Messages, size: size}
	c.includeUsage = req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	s.serve(w, r, c)
}

func openAIUsage(u Usage) map[string]int {
	return map[string]int{
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.PromptTokens + u.CompletionTokens,
	}
}

func openAIMessage(c conversation, rp reply) map[string]any {
	msg := map[string]any{"role": "assistant", "content": nil}
	if rp.text != "" {
		msg["content"] = rp.text
	}
	if len(rp.calls) > 0 {
		calls := make([]map[string]any, 0, len(rp.calls))
		for _, tc := range rp.calls {
			calls = append(calls, map[string]any{
				"id":       tc.id,
				"type":     "function",
				"function": map[string]string{"name": tc.name, "arguments": tc.args},
			})
		}
		msg["tool_calls"] = calls
	}
	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-fakellm-%d", rp.seq),
		"object":  "chat.completion",
		"created": rp.record.Time.Unix(),
		"model":   c.model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finishReason(c.api, rp)}},
		"usage":   openAIUsage(rp.usage),
	}
}

func streamOpenAI(sw *streamWriter, c conversation, rp reply) {
	id := fmt.Sprintf("chatcmpl-fakellm-%d", rp.seq)
	created := rp.record.Time.Unix()
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   c.model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	sw.send("", chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	for _, w := range words(rp.text) {
		sw.send("", chunk(map[string]any{"content": w}, nil))
	}
	for i, tc := range rp.calls {
		sw.send("", chunk(map[string]any{"tool_calls": []map[string]any{{
			"index":    i,
			"id":       tc.id,
			"type":     "function",
			"function": map[string]string{"name": tc.name, "arguments": ""},
		}}}, nil))
		for _, p := range pieces(tc.args, argumentPiece) {
			sw.send("", chunk(map[string]any{"tool_calls": []map[string]any{{
				"index":    i,
				"function": map[string]string{"arguments": p},
			}}}, nil))
		}
	}
	final := chunk(map[string]any{}, finishReason(c.api, rp))
	if !c.includeUsage {
		final["usage"] = openAIUsage(rp.usage)
	}
	sw.send("", final)
	if c.includeUsage {
		usage := chunk(nil, nil)
		usage["choices"] = []any{}
		usage["usage"] = openAIUsage(rp.usage)
		sw.send("", usage)
	}
	sw.send("", "[DONE]")
}
