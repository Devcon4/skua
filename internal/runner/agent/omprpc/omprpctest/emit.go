package omprpctest

const textDeltaMax = 64 << 10

func (r *run) text(s string) error {
	id := r.a.nextID("msg")
	r.a.out.send(map[string]any{"type": "message_start", "messageId": id, "message": map[string]any{"role": "assistant", "content": []any{}}})
	for off := 0; ; off += textDeltaMax {
		end := min(off+textDeltaMax, len(s))
		r.a.out.send(map[string]any{
			"type":                  "message_update",
			"messageId":             id,
			"assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 0, "delta": s[off:end]},
			"message":               map[string]any{"role": "assistant"},
		})
		if end == len(s) {
			break
		}
	}
	r.a.out.send(map[string]any{
		"type":      "message_end",
		"messageId": id,
		"message": map[string]any{
			"role":       "assistant",
			"content":    []any{map[string]any{"type": "text", "text": s}},
			"stopReason": "stop",
		},
	})
	r.a.mu.Lock()
	r.a.session.state.LastText = s
	r.a.mu.Unlock()
	return r.save()
}

type toolCall struct {
	r    *run
	id   string
	name string
	args any
}

func (r *run) startTool(name string, args any) *toolCall {
	tc := &toolCall{r: r, id: r.a.nextID("call"), name: name, args: args}
	r.a.out.send(map[string]any{"type": "tool_execution_start", "toolCallId": tc.id, "toolName": name, "args": args})
	return tc
}

func toolContent(text string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
}

func (tc *toolCall) update(partial string) {
	tc.r.a.out.send(map[string]any{
		"type": "tool_execution_update", "toolCallId": tc.id, "toolName": tc.name, "args": tc.args,
		"partialResult": toolContent(partial),
	})
}

func (tc *toolCall) end(result string, isError bool) {
	if tc.r.ctx.Err() != nil {
		result, isError = "Command aborted", true
	}
	tc.r.a.out.send(map[string]any{
		"type": "tool_execution_end", "toolCallId": tc.id, "toolName": tc.name,
		"result": toolContent(result), "isError": isError,
	})
	if tc.r.ctx.Err() == nil {
		tc.r.deliverSteer()
	}
}

func (r *run) deliverSteer() bool {
	msg, rest, ok := r.a.popSteer()
	if !ok {
		return false
	}
	user := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": msg}}}
	id := r.a.nextID("msg")
	r.a.out.send(map[string]any{"type": "message_start", "messageId": id, "message": user})
	r.a.out.send(map[string]any{"type": "message_end", "messageId": id, "message": user})
	r.a.queueUpdate(rest)
	_ = r.text("steer-received: " + msg)
	return true
}

func (r *run) bash(command string, fn func(tc *toolCall) (string, error)) error {
	tc := r.startTool("bash", map[string]any{"command": command})
	out, err := fn(tc)
	if err != nil {
		tc.end(out+err.Error(), true)
		return err
	}
	tc.end(out, false)
	return nil
}
