package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Devcon4/skua/internal/contract/wire"
)

func (e *runnerTestEnv) request(t *testing.T, method, path, token, version string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.url+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if version != "" {
		req.Header.Set(wire.HeaderRunnerVersion, version)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(out)
}

type runnerStreamItem struct {
	heartbeat bool
	id        int64
	typ       string
	data      string
}

type runnerTestStream struct {
	items  chan runnerStreamItem
	cancel context.CancelFunc
	done   chan struct{}
}

func (e *runnerTestEnv) openStream(t *testing.T, token string, lastID int64) *runnerTestStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url+wire.RunnerStreamPath, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(wire.HeaderRunnerVersion, runnerTestVersion)
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	}
	resp, err := e.http.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("open stream: %d %s", resp.StatusCode, body)
	}
	s := &runnerTestStream{items: make(chan runnerStreamItem, 64), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer close(s.items)
		defer func() { _ = resp.Body.Close() }()
		br := bufio.NewReader(resp.Body)
		var cur runnerStreamItem
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case line == ": hb":
				s.items <- runnerStreamItem{heartbeat: true}
			case line == "":
				if cur.typ != "" {
					s.items <- cur
				}
				cur = runnerStreamItem{}
			case strings.HasPrefix(line, "id: "):
				cur.id, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "event: "):
				cur.typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data += strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	t.Cleanup(s.close)
	return s
}

func (s *runnerTestStream) close() {
	s.cancel()
	<-s.done
}

func (s *runnerTestStream) next(t *testing.T) runnerStreamItem {
	t.Helper()
	select {
	case it, ok := <-s.items:
		if !ok {
			t.Fatal("stream closed")
		}
		return it
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for a stream item")
	}
	return runnerStreamItem{}
}

func (s *runnerTestStream) command(t *testing.T, typ string, v any) int64 {
	t.Helper()
	for {
		it := s.next(t)
		if it.heartbeat {
			continue
		}
		if it.typ != typ {
			t.Fatalf("got command %s %s, want %s", it.typ, it.data, typ)
		}
		if err := json.Unmarshal([]byte(it.data), v); err != nil {
			t.Fatalf("decode %s: %v", typ, err)
		}
		return it.id
	}
}

func (e *runnerTestEnv) heartbeat(t *testing.T, s *runnerTestStream) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		e.clock.Step(wire.HeartbeatInterval)
		select {
		case it, ok := <-s.items:
			if !ok {
				t.Fatal("stream closed")
			}
			if !it.heartbeat {
				t.Fatalf("got command %s %s before a heartbeat", it.typ, it.data)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("no heartbeat")
}

func runnerEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runnerWantRefusal(t *testing.T, code int, body string, wantCode int, wantReason string) {
	t.Helper()
	if code != wantCode || !strings.HasPrefix(body, wantReason) {
		t.Fatalf("got %d %q, want %d %s", code, body, wantCode, wantReason)
	}
}
