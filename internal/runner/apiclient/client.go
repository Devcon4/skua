// Package apiclient is the runner's client for skua-api's runner endpoints: the command stream, the event
// outbox and result submission.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/Devcon4/skua/internal/contract/wire"
)

const reasonLimit = 4 << 10

// ErrTooLarge is returned by SubmitResult when skua-api refuses the result with 413.
var ErrTooLarge = errors.New("apiclient: result too large")

// ErrRunUnknown is returned by SubmitResult when emitted events wait for a run name that neither an assign
// command nor SetRun supplied.
var ErrRunUnknown = errors.New("apiclient: run unknown, events cannot be sent")

type options struct {
	flushEvery  time.Duration
	batchEvents int
	batchBytes  int
	maxUnacked  int
	backoffMin  time.Duration
	backoffMax  time.Duration
	streamIdle  time.Duration
	log         *slog.Logger
}

func defaultOptions() options {
	return options{
		flushEvery:  100 * time.Millisecond,
		batchEvents: 64,
		batchBytes:  256 << 10,
		maxUnacked:  8 << 20,
		backoffMin:  200 * time.Millisecond,
		backoffMax:  5 * time.Second,
		streamIdle:  3 * wire.HeartbeatInterval,
		log:         slog.Default(),
	}
}

// retryBackoff sleeps uniformly between d/2 and d, where d doubles from backoffMin up to backoffMax.
func (o options) retryBackoff() wait.Backoff {
	return wait.Backoff{Duration: o.backoffMin / 2, Factor: 2, Jitter: 1, Cap: o.backoffMax / 2, Steps: math.MaxInt}
}

// Client talks to skua-api as one runner pod. Every request re-reads the token file and sends the runner version.
type Client struct {
	base      string
	tokenPath string
	version   string
	hc        *http.Client
	opt       options

	mu          sync.Mutex
	cond        *sync.Cond
	run         string
	lastEventID int64
	path        string
	file        *os.File
	seq         int64
	pending     []entry
	unacked     int
	drained     []chan struct{}
	closed      bool
	wake        chan struct{}
	stop        context.CancelFunc
	sendDone    chan struct{}
}

// New returns a client for the skua-api at baseURL. It reloads unacked events and the seq counter from
// stateDir and starts sending them; Close stops the sender. A nil hc uses a client without timeouts.
func New(baseURL, tokenPath, stateDir string, version string, hc *http.Client) *Client {
	return newClient(baseURL, tokenPath, stateDir, version, hc, defaultOptions())
}

func newClient(baseURL, tokenPath, stateDir, version string, hc *http.Client, opt options) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	ctx, stop := context.WithCancel(context.Background())
	c := &Client{
		base:      strings.TrimRight(baseURL, "/"),
		tokenPath: tokenPath,
		version:   version,
		hc:        hc,
		opt:       opt,
		path:      filepath.Join(stateDir, outboxName),
		wake:      make(chan struct{}, 1),
		stop:      stop,
		sendDone:  make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	if err := c.loadOutbox(stateDir); err != nil {
		c.opt.log.Error("runner outbox file unavailable, events kept in memory only", "path", c.path, "err", err)
	}
	go c.sendLoop(ctx)
	return c
}

// SetRun names the run for events emitted without one, for a runner that knows its run before any assign
// command arrives. An empty name is ignored.
func (c *Client) SetRun(name string) {
	if name == "" {
		return
	}
	c.mu.Lock()
	c.run = name
	c.mu.Unlock()
	c.signal()
}

// SetLastEventID makes the command stream resume after command id, so no command up to id is delivered.
// It applies to streams opened by a later Commands call.
func (c *Client) SetLastEventID(id int64) {
	c.mu.Lock()
	c.lastEventID = id
	c.mu.Unlock()
}

func (c *Client) postEvents(ctx context.Context, run string, evs []wire.RunnerEvent) (int64, error) {
	body, err := marshal(wire.RunnerEventsRequest{Run: run, Events: evs})
	if err != nil {
		return 0, fmt.Errorf("apiclient: encode events: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, wire.RunnerEventsPath, body)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, refusal(resp)
	}
	var a wire.Ack
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return 0, fmt.Errorf("apiclient: decode ack: %w", err)
	}
	return a.Ack, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return c.hc.Do(req)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	tok, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("apiclient: read token: %w", err)
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	req.Header.Set(wire.HeaderRunnerVersion, c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

type statusError struct {
	code   int
	reason string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("apiclient: refused with %d: %s", e.code, e.reason)
}

func refusal(resp *http.Response) error {
	return &statusError{code: resp.StatusCode, reason: readReason(resp.Body)}
}

func readReason(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, reasonLimit))
	return strings.TrimSpace(string(b))
}

func retryable(err error) bool {
	if errors.Is(err, ErrTooLarge) {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.code >= http.StatusInternalServerError
	}
	return true
}

func permanentRefusal(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.code {
	case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge:
		return true
	}
	return false
}

// marshal encodes v without HTML escaping, so the bytes sent are the bytes the runner measured.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
