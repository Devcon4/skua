package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/apiclient/apiclienttest"
)

const testRun = "p-0123456789"

type env struct {
	srv       *apiclienttest.Server
	stateDir  string
	tokenPath string
}

func setup(t *testing.T) env {
	t.Helper()
	dir := t.TempDir()
	e := env{srv: apiclienttest.New(t), stateDir: filepath.Join(dir, "run"), tokenPath: filepath.Join(dir, "token")}
	writeToken(t, e.tokenPath, "token-1")
	return e
}

func testOptions() options {
	o := defaultOptions()
	o.flushEvery = time.Hour
	o.backoffMin = 10 * time.Millisecond
	o.backoffMax = 40 * time.Millisecond
	o.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return o
}

func (e env) start(t *testing.T, o options) *Client {
	t.Helper()
	c := newClient(e.srv.URL+"/", e.tokenPath, e.stateDir, "0.1.0", nil, o)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func commands(t *testing.T, c *Client) <-chan wire.Command {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return c.Commands(ctx)
}

func (e env) assign(t *testing.T, c *Client) <-chan wire.Command {
	t.Helper()
	cmds := commands(t, c)
	data, err := json.Marshal(wire.Assign{Run: testRun})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Send(wire.Command{Type: wire.CommandAssign, Data: data})
	if got := recv(t, cmds); got.Type != wire.CommandAssign {
		t.Fatalf("first command = %q, want assign", got.Type)
	}
	return cmds
}

func steer(i int) wire.Command {
	data, _ := json.Marshal(wire.Steer{Message: fmt.Sprintf("m%d", i)})
	return wire.Command{Type: wire.CommandSteer, Data: data}
}

func recv(t *testing.T, ch <-chan wire.Command) wire.Command {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("command channel closed")
		}
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no command within 5s")
	}
	return wire.Command{}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeToken(t *testing.T, path, tok string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func textEvent(s string) wire.Event {
	data, _ := json.Marshal(wire.TextData{Delta: s})
	return wire.Event{Type: wire.EventText, Data: data}
}

func emit(c *Client, from, to int) {
	for i := from; i <= to; i++ {
		c.Emit(textEvent(fmt.Sprintf("e%d", i)))
	}
}

func seqs(evs []wire.RunnerEvent) []int64 {
	out := make([]int64, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Seq)
	}
	return out
}

func requests(srv *apiclienttest.Server, path string) []apiclienttest.Request {
	var out []apiclienttest.Request
	for _, r := range srv.Requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func statuses(rs []apiclienttest.Request) []int {
	out := make([]int, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Status)
	}
	return out
}

func outboxSeqs(t *testing.T, dir string) []int64 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, outboxName))
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		var r record
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("outbox line %q: %v", line, err)
		}
		if r.Type != "" {
			out = append(out, r.Seq)
		}
	}
	return out
}

func flush(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func unacked(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unacked
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestReconnectSendsLastCommandID(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	cmds := commands(t, c)
	e.srv.Send(steer(1))
	e.srv.Send(steer(2))
	recv(t, cmds)
	recv(t, cmds)
	e.srv.DropStreamAfter(1)
	e.srv.Send(steer(3))
	if got := recv(t, cmds); got.ID != 3 {
		t.Fatalf("command after reconnect has id %d, want 3", got.ID)
	}
	var ids []string
	for _, r := range requests(e.srv, wire.RunnerStreamPath) {
		if r.Status == http.StatusOK {
			ids = append(ids, r.LastEventID)
		}
	}
	if !slices.Equal(ids, []string{"", "2"}) {
		t.Fatalf("Last-Event-ID per stream = %q, want [\"\" \"2\"]", ids)
	}
}

func TestDroppedStreamMidEventLosesNoCommand(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	e.srv.DropStreamAfter(3)
	for i := 1; i <= 5; i++ {
		e.srv.Send(steer(i))
	}
	cmds := commands(t, c)
	for i := 1; i <= 5; i++ {
		got := recv(t, cmds)
		want := steer(i)
		if got.ID != int64(i) || got.Type != want.Type || !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("command %d = %+v, want id %d %s %s", i, got, i, want.Type, want.Data)
		}
	}
	select {
	case got := <-cmds:
		t.Fatalf("unexpected extra command %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	streams := requests(e.srv, wire.RunnerStreamPath)
	if len(streams) != 2 || streams[1].LastEventID != "3" {
		t.Fatalf("stream requests = %+v, want a resume from 3", streams)
	}
}

func TestRefusedStreamRetriedWithBackoff(t *testing.T) {
	e := setup(t)
	logs := &syncBuffer{}
	o := defaultOptions()
	o.log = slog.New(slog.NewTextHandler(logs, nil))
	c := e.start(t, o)
	e.srv.RefuseStream(http.StatusConflict, "stream active", 2)
	e.srv.Send(steer(1))
	if got := recv(t, commands(t, c)); got.ID != 1 {
		t.Fatalf("command id %d, want 1", got.ID)
	}
	streams := requests(e.srv, wire.RunnerStreamPath)
	if got := statuses(streams); !slices.Equal(got, []int{409, 409, 200}) {
		t.Fatalf("stream statuses = %v, want [409 409 200]", got)
	}
	if gap := streams[1].At.Sub(streams[0].At); gap < 100*time.Millisecond {
		t.Fatalf("first retry after %v, want at least 100ms", gap)
	}
	if gap := streams[2].At.Sub(streams[1].At); gap < 200*time.Millisecond {
		t.Fatalf("second retry after %v, want at least 200ms", gap)
	}
	if !strings.Contains(logs.String(), "stream active") {
		t.Fatalf("refusal reason not logged: %s", logs.String())
	}
}

func TestRotatedTokenUsedOnNextRequest(t *testing.T) {
	e := setup(t)
	o := testOptions()
	o.batchEvents = 1
	c := e.start(t, o)
	cmds := e.assign(t, c)
	emit(c, 1, 1)
	eventually(t, "first event", func() bool { return len(e.srv.Events(testRun)) == 1 })
	writeToken(t, e.tokenPath, "token-2")
	emit(c, 2, 2)
	eventually(t, "second event", func() bool { return len(e.srv.Events(testRun)) == 2 })
	e.srv.DropStreamAfter(1)
	e.srv.Send(steer(1))
	recv(t, cmds)
	var posts []string
	for _, r := range requests(e.srv, wire.RunnerEventsPath) {
		posts = append(posts, r.Token)
	}
	if !slices.Equal(posts, []string{"token-1", "token-2"}) {
		t.Fatalf("event post tokens = %v, want [token-1 token-2]", posts)
	}
	streams := requests(e.srv, wire.RunnerStreamPath)
	if got := streams[len(streams)-1].Token; got != "token-2" {
		t.Fatalf("reconnect token = %q, want token-2", got)
	}
	for _, r := range e.srv.Requests() {
		if r.Version != "0.1.0" {
			t.Fatalf("request %s carries version %q", r.Path, r.Version)
		}
	}
}

func TestFailedEventPostsStayInOutboxUntilAcked(t *testing.T) {
	e := setup(t)
	o := testOptions()
	o.batchEvents = 3
	o.backoffMin = 200 * time.Millisecond
	o.backoffMax = 200 * time.Millisecond
	c := e.start(t, o)
	e.srv.FailEvents(http.StatusServiceUnavailable, 2)
	emit(c, 1, 3)
	e.assign(t, c)
	eventually(t, "a failed post", func() bool { return len(e.srv.EventPosts()) >= 1 })
	if got := outboxSeqs(t, e.stateDir); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("outbox after a failed post = %v, want [1 2 3]", got)
	}
	flush(t, c)
	posts := e.srv.EventPosts()
	var got []int
	for _, p := range posts {
		got = append(got, p.Status)
		if !slices.Equal(seqs(p.Body.Events), []int64{1, 2, 3}) || p.Body.Run != testRun {
			t.Fatalf("post %+v, want run %s seqs [1 2 3]", p.Body, testRun)
		}
	}
	if !slices.Equal(got, []int{503, 503, 200}) {
		t.Fatalf("event post statuses = %v, want [503 503 200]", got)
	}
	if got := seqs(e.srv.Events(testRun)); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("stored seqs = %v", got)
	}
	if got := outboxSeqs(t, e.stateDir); len(got) != 0 {
		t.Fatalf("outbox after ack = %v, want empty", got)
	}
	if n := unacked(c); n != 0 {
		t.Fatalf("unacked bytes = %d after ack", n)
	}
}

func TestDuplicatePostAfterDroppedResponseAckedOnce(t *testing.T) {
	e := setup(t)
	o := testOptions()
	o.batchEvents = 2
	c := e.start(t, o)
	e.srv.DropEventsResponse(1)
	emit(c, 1, 2)
	e.assign(t, c)
	flush(t, c)
	posts := e.srv.EventPosts()
	if len(posts) != 2 {
		t.Fatalf("event posts = %d, want 2", len(posts))
	}
	for _, p := range posts {
		if !slices.Equal(seqs(p.Body.Events), []int64{1, 2}) {
			t.Fatalf("posted seqs %v, want [1 2]", seqs(p.Body.Events))
		}
	}
	if posts[0].Status != 0 || posts[1].Status != http.StatusOK {
		t.Fatalf("post statuses = %d %d, want dropped then 200", posts[0].Status, posts[1].Status)
	}
	if got := seqs(e.srv.Events(testRun)); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("stored seqs = %v, want [1 2]", got)
	}
	if got := outboxSeqs(t, e.stateDir); len(got) != 0 {
		t.Fatalf("outbox = %v, want empty", got)
	}
}

func TestRebuiltClientRepostsUnackedEvents(t *testing.T) {
	e := setup(t)
	o := testOptions()
	o.batchEvents = 1
	c1 := e.start(t, o)
	e.assign(t, c1)
	e.srv.FailEvents(http.StatusServiceUnavailable, 1<<30)
	emit(c1, 1, 3)
	eventually(t, "a failed post", func() bool { return len(e.srv.EventPosts()) >= 1 })
	if err := c1.Close(); err != nil {
		t.Fatal(err)
	}
	if got := outboxSeqs(t, e.stateDir); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("outbox after close = %v, want [1 2 3]", got)
	}
	failed := len(e.srv.EventPosts())
	e.srv.FailEvents(http.StatusServiceUnavailable, 0)

	c2 := e.start(t, o)
	flush(t, c2)
	stored := e.srv.Events(testRun)
	if got := seqs(stored); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("stored seqs = %v, want [1 2 3]", got)
	}
	for i, ev := range stored {
		want := textEvent(fmt.Sprintf("e%d", i+1))
		if ev.Type != want.Type || !bytes.Equal(ev.Data, want.Data) {
			t.Fatalf("stored event %d = %+v, want %+v", i+1, ev, want)
		}
	}
	for _, p := range e.srv.EventPosts()[failed:] {
		if p.Body.Run != testRun {
			t.Fatalf("re-posted for run %q, want %q", p.Body.Run, testRun)
		}
	}
	emit(c2, 4, 4)
	flush(t, c2)
	if err := c2.Close(); err != nil {
		t.Fatal(err)
	}

	c3 := e.start(t, o)
	emit(c3, 5, 5)
	flush(t, c3)
	if got := seqs(e.srv.Events(testRun)); !slices.Equal(got, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("stored seqs = %v, want [1 2 3 4 5]", got)
	}
}

func TestEmitBlocksAtByteCapUntilAck(t *testing.T) {
	e := setup(t)
	o := testOptions()
	o.batchEvents = 1
	o.maxUnacked = 1024
	c := e.start(t, o)
	e.assign(t, c)
	e.srv.FailEvents(http.StatusServiceUnavailable, 1<<30)
	big := strings.Repeat("x", 400)
	for range 3 {
		c.Emit(textEvent(big))
	}
	if n := unacked(c); n < o.maxUnacked {
		t.Fatalf("unacked bytes = %d, want at least %d", n, o.maxUnacked)
	}
	done := make(chan struct{})
	go func() {
		c.Emit(textEvent(big))
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Emit returned above the byte cap")
	case <-time.After(300 * time.Millisecond):
	}
	e.srv.FailEvents(http.StatusServiceUnavailable, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Emit still blocked after the ack")
	}
	flush(t, c)
	if got := seqs(e.srv.Events(testRun)); !slices.Equal(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("stored seqs = %v, want [1 2 3 4]", got)
	}
}

func TestSubmitResultAfterOutboxEmpty(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	e.assign(t, c)
	e.srv.FailEvents(http.StatusServiceUnavailable, 2)
	e.srv.FailResult(http.StatusInternalServerError, 2)
	emit(c, 1, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := wire.RunnerResult{Run: testRun, Phase: "Succeeded", Reason: "Completed", Result: json.RawMessage(`"done"`)}
	if err := c.SubmitResult(ctx, res); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	results := e.srv.Results()
	if len(results) != 1 || results[0].StoredEvents != 3 || string(results[0].Result.Result) != `"done"` {
		t.Fatalf("results = %+v, want one result after 3 stored events", results)
	}
	if got := statuses(requests(e.srv, wire.RunnerResultPath)); !slices.Equal(got, []int{500, 500, 200}) {
		t.Fatalf("result statuses = %v, want [500 500 200]", got)
	}
	if got := outboxSeqs(t, e.stateDir); len(got) != 0 {
		t.Fatalf("outbox = %v, want empty", got)
	}
}

func TestSubmitResultTooLarge(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	e.srv.MaxResultBytes(16)
	res := wire.RunnerResult{Run: testRun, Phase: "Succeeded", Reason: "Completed", Result: json.RawMessage(`"` + strings.Repeat("y", 64) + `"`)}
	err := c.SubmitResult(context.Background(), res)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("SubmitResult error = %v, want ErrTooLarge", err)
	}
	if got := statuses(requests(e.srv, wire.RunnerResultPath)); !slices.Equal(got, []int{413}) {
		t.Fatalf("result statuses = %v, want [413]", got)
	}
	if len(e.srv.Results()) != 0 {
		t.Fatal("oversize result was stored")
	}
}

func TestKnownRunSubmitsWithoutAssign(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	c.SetRun(testRun)
	emit(c, 1, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := wire.RunnerResult{Run: testRun, Phase: "Succeeded", Reason: "Completed", Result: json.RawMessage(`"done"`)}
	if err := c.SubmitResult(ctx, res); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	if results := e.srv.Results(); len(results) != 1 || results[0].StoredEvents != 2 {
		t.Fatalf("results = %+v, want one result after 2 stored events", results)
	}
	for _, p := range e.srv.EventPosts() {
		if p.Body.Run != testRun {
			t.Fatalf("events posted for run %q, want %q", p.Body.Run, testRun)
		}
	}
	if n := len(requests(e.srv, wire.RunnerStreamPath)); n != 0 {
		t.Fatalf("stream requests = %d, want 0", n)
	}
}

func TestSubmitResultWithUnknownRunFails(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	emit(c, 1, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := wire.RunnerResult{Run: testRun, Phase: "Succeeded", Reason: "Completed"}
	if err := c.SubmitResult(ctx, res); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("SubmitResult error = %v, want ErrRunUnknown", err)
	}
	if n := len(requests(e.srv, wire.RunnerResultPath)) + len(e.srv.EventPosts()); n != 0 {
		t.Fatalf("requests to result and events = %d, want 0", n)
	}
	if got := outboxSeqs(t, e.stateDir); !slices.Equal(got, []int64{1}) {
		t.Fatalf("outbox = %v, want [1]", got)
	}
}

func TestSetLastEventIDResumesStreamAfterIt(t *testing.T) {
	e := setup(t)
	for i := range 3 {
		e.srv.Send(steer(i + 1))
	}
	c := e.start(t, testOptions())
	c.SetLastEventID(2)
	cmds := commands(t, c)
	if got := recv(t, cmds); got.ID != 3 {
		t.Fatalf("first command has id %d, want 3", got.ID)
	}
	var ids []string
	for _, r := range requests(e.srv, wire.RunnerStreamPath) {
		ids = append(ids, r.LastEventID)
	}
	if !slices.Equal(ids, []string{"2"}) {
		t.Fatalf("Last-Event-ID per stream = %q, want [\"2\"]", ids)
	}
}

func TestOversizeEventDoesNotBlockLaterEventsOrResult(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	e.assign(t, c)
	big, err := json.Marshal(wire.TextData{Delta: strings.Repeat("x", 2<<20)})
	if err != nil {
		t.Fatal(err)
	}
	c.Emit(textEvent("before"))
	c.Emit(wire.Event{Type: wire.EventText, Data: big})
	c.Emit(textEvent("after"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.SubmitResult(ctx, wire.RunnerResult{Run: testRun, Phase: "Succeeded", Reason: "Completed", Result: json.RawMessage(`"done"`)}); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	evs := e.srv.Events(testRun)
	if !slices.Equal(seqs(evs), []int64{1, 2, 3}) {
		t.Fatalf("stored seqs = %v, want [1 2 3]", seqs(evs))
	}
	var marker string
	if evs[1].Type != wire.EventText || json.Unmarshal(evs[1].Data, &marker) != nil || !strings.Contains(marker, fmt.Sprintf(" %d bytes", len(big))) {
		t.Fatalf("oversize event stored as %s %.200s, want a string marker naming %d bytes", evs[1].Type, evs[1].Data, len(big))
	}
	if results := e.srv.Results(); len(results) != 1 || results[0].StoredEvents != 3 {
		t.Fatalf("results = %+v, want one after 3 stored events", results)
	}
}

func TestPermanentlyRefusedEventsDroppedNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			e := setup(t)
			c := e.start(t, testOptions())
			e.srv.FailEvents(code, 1)
			emit(c, 1, 2)
			e.assign(t, c)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := c.SubmitResult(ctx, wire.RunnerResult{Run: testRun, Phase: "Succeeded", Reason: "Completed", Result: json.RawMessage(`"done"`)}); err != nil {
				t.Fatalf("SubmitResult: %v", err)
			}
			var got []int
			for _, p := range e.srv.EventPosts() {
				got = append(got, p.Status)
			}
			if !slices.Equal(got, []int{code}) {
				t.Fatalf("event post statuses = %v, want [%d]", got, code)
			}
			if got := outboxSeqs(t, e.stateDir); len(got) != 0 {
				t.Fatalf("outbox = %v, want empty", got)
			}
			if n := unacked(c); n != 0 {
				t.Fatalf("unacked bytes = %d", n)
			}
		})
	}
}

func TestSubmitResultSendsMeasuredBytes(t *testing.T) {
	e := setup(t)
	c := e.start(t, testOptions())
	result := json.RawMessage(`{"html":"<b>a & b</b>"}`)
	e.srv.MaxResultBytes(len(result))
	if err := c.SubmitResult(context.Background(), wire.RunnerResult{Run: testRun, Phase: "Succeeded", Reason: "Completed", Result: result}); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	if results := e.srv.Results(); len(results) != 1 || !bytes.Equal(results[0].Result.Result, result) {
		t.Fatalf("results = %+v, want %s byte for byte", results, result)
	}
}
