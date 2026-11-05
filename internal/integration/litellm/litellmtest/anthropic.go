package litellmtest

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string    `json:"model"`
		Stream   bool      `json:"stream"`
		Messages []message `json:"messages"`
	}
	size, ok := decode(w, r, apiAnthropic, &req)
	if !ok {
		return
	}
	s.serve(w, r, conversation{api: apiAnthropic, model: req.Model, stream: req.Stream, messages: req.Messages, size: size})
}

func anthropicMessage(c conversation, rp reply) map[string]any {
	content := make([]map[string]any, 0, len(rp.calls)+1)
	if rp.text != "" {
		content = append(content, map[string]any{"type": "text", "text": rp.text})
	}
	for _, tc := range rp.calls {
		content = append(content, map[string]any{"type": "tool_use", "id": tc.id, "name": tc.name, "input": json.RawMessage(tc.args)})
	}
	return map[string]any{
		"id":            fmt.Sprintf("msg_fakellm_%d", rp.seq),
		"type":          "message",
		"role":          "assistant",
		"model":         c.model,
		"content":       content,
		"stop_reason":   finishReason(c.api, rp),
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": rp.usage.PromptTokens, "output_tokens": rp.usage.CompletionTokens},
	}
}

func streamAnthropic(sw *streamWriter, c conversation, rp reply) {
	sw.send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            fmt.Sprintf("msg_fakellm_%d", rp.seq),
			"type":          "message",
			"role":          "assistant",
			"model":         c.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": rp.usage.PromptTokens, "output_tokens": 1},
		},
	})
	index := 0
	if rp.text != "" {
		sw.send("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}})
		for _, w := range words(rp.text) {
			sw.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": w}})
		}
		sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	for _, tc := range rp.calls {
		sw.send("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": tc.id, "name": tc.name, "input": map[string]any{}}})
		for _, p := range pieces(tc.args, argumentPiece) {
			sw.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": p}})
		}
		sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	sw.send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": finishReason(c.api, rp), "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": rp.usage.CompletionTokens},
	})
	sw.send("message_stop", map[string]any{"type": "message_stop"})
}
