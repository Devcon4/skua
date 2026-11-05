package omprpctest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	scriptPrefix     = "SCRIPT:"
	gatePollInterval = 10 * time.Millisecond
)

type run struct {
	a   *agent
	ctx context.Context
}

func (r *run) state() *sessionState {
	return &r.a.session.state
}

func (r *run) save() error {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.a.session.save()
}

// scriptOf returns the prompt from its first line that starts with SCRIPT: (after trimming), with the prefix
// removed, and false when no line does.
func scriptOf(message string) (string, bool) {
	for rest := message; rest != ""; {
		line, next, _ := strings.Cut(rest, "\n")
		if strings.HasPrefix(strings.TrimSpace(line), scriptPrefix) {
			return strings.TrimPrefix(strings.TrimSpace(rest), scriptPrefix), true
		}
		rest = next
	}
	return "", false
}

func (r *run) execute(message string) error {
	st := r.state()
	if line, ok := scriptOf(message); ok {
		r.a.mu.Lock()
		*st = sessionState{ID: st.ID, Provider: st.Provider, ModelID: st.ModelID, Script: line, LastText: st.LastText}
		r.a.mu.Unlock()
		if err := r.save(); err != nil {
			return err
		}
	} else if st.Script == "" || st.Finished {
		return r.text("ok")
	}
	name, rest, _ := strings.Cut(strings.TrimSpace(st.Script), " ")
	rest = strings.TrimSpace(rest)
	err := r.dispatch(name, rest)
	if err == nil && r.ctx.Err() == nil {
		st.Finished = true
		return r.save()
	}
	return err
}

func (r *run) dispatch(name, rest string) error {
	args := strings.Fields(rest)
	switch name {
	case "reply":
		return r.text(rest)
	case "reply-bytes":
		n, err := intArg(args, 0)
		if err != nil {
			return err
		}
		return r.text(strings.Repeat("x", n))
	case "steps":
		return r.steps(args)
	case "sleep":
		return r.sleep(args)
	case "orphan-then-crash":
		return r.orphanThenCrash()
	case "term-runner":
		return r.termRunner()
	case "fill":
		return r.fill(args)
	case "error":
		return scriptedError(args)
	case "llm":
		return r.llm(args)
	case "llm-loop":
		return r.llmLoop(args)
	case "submit":
		return r.submit(rest)
	case "no-submit":
		return r.text("no result submitted")
	case "git-push":
		if len(args) != 1 {
			return errors.New("git-push needs <file>")
		}
		return r.gitPush(args[0])
	case "push-and-pr":
		return r.pushAndPR(args)
	case "gitloop":
		return r.gitloop(args)
	case "push-main":
		return r.pushMain()
	default:
		return fmt.Errorf("unknown script %q", name)
	}
}

func intArg(args []string, i int) (int, error) {
	if len(args) <= i {
		return 0, fmt.Errorf("missing argument %d", i+1)
	}
	return strconv.Atoi(args[i])
}

func durArg(args []string, i int) (time.Duration, error) {
	if len(args) <= i {
		return 0, fmt.Errorf("missing argument %d", i+1)
	}
	if f, err := strconv.ParseFloat(args[i], 64); err == nil {
		return time.Duration(f * float64(time.Second)), nil
	}
	return time.ParseDuration(args[i])
}

func (r *run) wait(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.ctx.Done():
		return false
	}
}

func (r *run) steps(args []string) error {
	n, err := intArg(args, 0)
	if err != nil {
		return err
	}
	interval, err := durArg(args, 1)
	if err != nil {
		return err
	}
	gate := ""
	if len(args) > 2 {
		gate = args[2]
	}
	st := r.state()
	for k := st.Progress + 1; k <= n; k++ {
		label := fmt.Sprintf("step %d", k)
		tc := r.startTool("bash", map[string]any{"command": "echo " + label})
		tc.update(label + " running")
		if !r.wait(interval) || !r.awaitFile(gate) {
			tc.end("", false)
			return nil
		}
		gate = ""
		tc.end(label, false)
		if err := r.text(label); err != nil {
			return err
		}
		st.Progress = k
		if err := r.save(); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) awaitFile(path string) bool {
	for path != "" {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if !r.wait(gatePollInterval) {
			return false
		}
	}
	return true
}

func (r *run) sleep(args []string) error {
	d, err := durArg(args, 0)
	if err != nil {
		return err
	}
	secs := strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
	err = r.bash("sleep "+secs, func(*toolCall) (string, error) {
		out, err := exec.CommandContext(r.ctx, "sleep", secs).CombinedOutput()
		return string(out), err
	})
	if r.ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return err
	}
	return r.text("slept " + d.String())
}
