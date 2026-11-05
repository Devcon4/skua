package omprpctest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

type agent struct {
	out         *frameWriter
	supported   []int
	settleDelay time.Duration
	exit        chan int

	mu       sync.Mutex
	session  *session
	steering []string
	turn     *turn
	ids      int
}

func (a *agent) nextID(prefix string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ids++
	return fmt.Sprintf("%s-%d", prefix, a.ids)
}

func (a *agent) serve(stdin io.Reader) int {
	a.out.send(map[string]any{
		"type":                      "ready",
		"protocolVersion":           1,
		"supportedProtocolVersions": a.supported,
		"maxFrameBytes":             maxFrameBytes,
		"maxReassembledFrameBytes":  maxReassembledFrameBytes,
	})
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 0, 64<<10), maxReassembledFrameBytes)
		for sc.Scan() {
			lines <- append([]byte(nil), sc.Bytes()...)
		}
	}()
	for {
		select {
		case code := <-a.exit:
			return code
		case line, ok := <-lines:
			if !ok {
				a.stopTurn()
				return 0
			}
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var c command
			if err := json.Unmarshal(line, &c); err != nil {
				a.out.respond("", "parse", nil, "Failed to parse command: "+err.Error())
				continue
			}
			a.dispatch(c)
		}
	}
}

func (a *agent) dispatch(c command) {
	switch c.Type {
	case "negotiate_protocol":
		v := c.ProtocolVersion
		if v == nil {
			v = c.Version
		}
		if v == nil || !slices.Contains(a.supported, *v) {
			n := 0
			if v != nil {
				n = *v
			}
			a.out.respond(c.ID, c.Type, nil, fmt.Sprintf("Unsupported RPC protocol version: %d", n))
			return
		}
		a.out.respond(c.ID, c.Type, map[string]any{"protocolVersion": *v}, "")
		if *v == 2 {
			a.out.enableV2()
		}
	case "open_session":
		a.openSession(c)
	case "set_subagent_subscription":
		a.out.respond(c.ID, c.Type, map[string]any{"level": c.Level}, "")
	case "prompt":
		a.prompt(c)
	case "steer", "follow_up":
		a.enqueueSteer(c.Message)
		a.out.respond(c.ID, c.Type, nil, "")
	case "abort":
		a.stopTurn()
		a.out.respond(c.ID, c.Type, nil, "")
	case "get_last_assistant_text":
		a.mu.Lock()
		text := a.session.state.LastText
		a.mu.Unlock()
		a.out.respond(c.ID, c.Type, map[string]any{"text": text}, "")
	case "get_state":
		a.out.respond(c.ID, c.Type, a.state(), "")
	default:
		a.out.respond(c.ID, c.Type, nil, "Unknown command: "+c.Type)
	}
}

func (a *agent) state() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.session.state
	return map[string]any{
		"model":          map[string]any{"provider": st.Provider, "id": st.ModelID},
		"isStreaming":    a.turn != nil,
		"isSettled":      a.turn == nil,
		"steeringMode":   "one-at-a-time",
		"followUpMode":   "one-at-a-time",
		"interruptMode":  "immediate",
		"sessionFile":    a.session.path,
		"sessionId":      st.ID,
		"queuedMessages": map[string]any{"steering": slices.Clone(a.steering), "followUp": []string{}},
	}
}

type turn struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

func (a *agent) prompt(c command) {
	a.mu.Lock()
	busy := a.turn != nil
	a.mu.Unlock()
	if busy {
		if c.StreamingBehavior == "" {
			a.out.respond(c.ID, c.Type, nil, "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.")
			return
		}
		a.enqueueSteer(c.Message)
		a.out.respond(c.ID, c.Type, map[string]any{"agentInvoked": true}, "")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &turn{id: c.ID, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	a.turn = t
	a.mu.Unlock()
	a.out.respond(c.ID, c.Type, map[string]any{"agentInvoked": true}, "")
	go a.runTurn(ctx, t, c.Message)
}

func (a *agent) stopTurn() {
	a.mu.Lock()
	t := a.turn
	a.mu.Unlock()
	if t == nil {
		return
	}
	t.cancel()
	<-t.done
}

func (a *agent) enqueueSteer(msg string) {
	a.mu.Lock()
	a.steering = append(a.steering, msg)
	q := slices.Clone(a.steering)
	a.mu.Unlock()
	a.queueUpdate(q)
}

func (a *agent) queueUpdate(steering []string) {
	if steering == nil {
		steering = []string{}
	}
	a.out.send(map[string]any{"type": "queue_update", "steering": steering, "followUp": []string{}})
}

func (a *agent) popSteer() (string, []string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.steering) == 0 {
		return "", nil, false
	}
	msg := a.steering[0]
	a.steering = a.steering[1:]
	return msg, slices.Clone(a.steering), true
}

func (a *agent) clearSteering() {
	a.mu.Lock()
	had := len(a.steering) > 0
	a.steering = nil
	a.mu.Unlock()
	if had {
		a.queueUpdate(nil)
	}
}

type promptError struct {
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
}

func (e *promptError) Error() string { return e.Message }

func (a *agent) runTurn(ctx context.Context, t *turn, message string) {
	defer close(t.done)
	r := &run{a: a, ctx: ctx}
	a.out.send(map[string]any{"type": "agent_start"})
	a.out.send(map[string]any{"type": "turn_start"})
	userMsg := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": message}}}
	id := a.nextID("msg")
	a.out.send(map[string]any{"type": "message_start", "messageId": id, "message": userMsg})
	a.out.send(map[string]any{"type": "message_end", "messageId": id, "message": userMsg})

	err := r.execute(message)
	if ctx.Err() == nil && err == nil {
		for r.deliverSteer() {
		}
	}
	a.out.send(map[string]any{"type": "turn_end"})
	a.out.send(map[string]any{"type": "agent_end", "isTerminal": true, "yielded": true})

	result := map[string]any{"type": "prompt_result"}
	if t.id != "" {
		result["id"] = t.id
	}
	var pe *promptError
	switch {
	case ctx.Err() != nil:
		result["status"] = "aborted"
		a.clearSteering()
	case errors.As(err, &pe):
		result["status"] = "error"
		result["error"] = pe
	case err != nil:
		result["status"] = "error"
		result["error"] = &promptError{Message: err.Error()}
	default:
		result["status"] = "completed"
	}
	settled := result["status"] != "error"
	result["sessionSettled"] = settled
	if settled {
		a.endTurn()
		a.out.send(result)
		return
	}
	a.out.send(result)
	r.wait(a.settleDelay)
	a.endTurn()
	a.out.send(map[string]any{"type": "session_settled"})
}

func (a *agent) endTurn() {
	a.mu.Lock()
	a.turn = nil
	a.mu.Unlock()
}
