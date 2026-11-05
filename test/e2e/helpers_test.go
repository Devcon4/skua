//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
)

const (
	runNamespace     = "skua"
	sandboxNamespace = "skua-sandboxes"

	dexIssuer      = "http://dex.127.0.0.1.nip.io:32000"
	dexHostPort    = "dex.127.0.0.1.nip.io:32000"
	dexNodePort    = "127.0.0.1:32000"
	ompClientID    = "skua-omp"
	ompRedirectURI = "http://127.0.0.1:8085/callback"

	apiBaseURL     = "http://127.0.0.1:32080/v1alpha1"
	litellmBaseURL = "http://127.0.0.1:32040"

	poll           = 2 * time.Second
	requestTimeout = 30 * time.Second
	eventsTimeout  = 15 * time.Minute
	podTimeout     = 3 * time.Minute
	profileTimeout = 5 * time.Minute

	conditionReady = "Ready"
)

var dexHTTP = &http.Client{Transport: dexTransport(), Timeout: requestTimeout}

func dexTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	var d net.Dialer
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == dexHostPort {
			addr = dexNodePort
		}
		return d.DialContext(ctx, network, addr)
	}
	return tr
}

func eventually(t *testing.T, what string, timeout time.Duration, check func(context.Context) (bool, string)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	last := ""
	for {
		ok, msg := check(ctx)
		if ok {
			return
		}
		if msg != last {
			t.Logf("%s: %s", what, msg)
			last = msg
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: timed out after %s; last: %s", what, timeout, last)
		case <-time.After(poll):
		}
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

func secretValue(t *testing.T, namespace, name, key string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	s, err := clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret %s/%s: %v", namespace, name, err)
	}
	v, ok := s.Data[key]
	if !ok {
		t.Fatalf("secret %s/%s has no key %s", namespace, name, key)
	}
	return string(v)
}

func dexProvider(t *testing.T) *oidc.Provider {
	t.Helper()
	ctx, cancel := context.WithTimeout(oidc.ClientContext(context.Background(), dexHTTP), requestTimeout)
	defer cancel()
	p, err := oidc.NewProvider(ctx, dexIssuer)
	if err != nil {
		t.Fatalf("dex discovery: %v", err)
	}
	return p
}

func login(t *testing.T, email string) string {
	t.Helper()
	user, _, _ := strings.Cut(email, "@")
	password := secretValue(t, "dex", "dex-test-users", user+"-password")
	endpoint := dexProvider(t).Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	conf := oauth2.Config{
		ClientID:    ompClientID,
		Endpoint:    endpoint,
		RedirectURL: ompRedirectURI,
		Scopes:      []string{oidc.ScopeOpenID, "email", "profile", "groups"},
	}

	verifier := oauth2.GenerateVerifier()
	state := randomHex(t, 8)
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	c := &http.Client{
		Transport: dexHTTP.Transport,
		Jar:       jar,
		Timeout:   requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if strings.HasPrefix(req.URL.String(), ompRedirectURI) {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}

	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatalf("dex authorize: %v", err)
	}
	formURL := resp.Request.URL.String()
	drain(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dex authorize: status %d at %s", resp.StatusCode, formURL)
	}

	resp, err = c.PostForm(formURL, url.Values{"login": {email}, "password": {password}})
	if err != nil {
		t.Fatalf("dex login: %v", err)
	}
	drain(resp)
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("dex login for %s did not redirect to the callback: status %d", email, resp.StatusCode)
	}
	q := loc.Query()
	if q.Get("state") != state {
		t.Fatalf("dex callback state %q, want %q", q.Get("state"), state)
	}
	code := q.Get("code")
	if code == "" {
		t.Fatalf("dex callback has no code: error %q: %s", q.Get("error"), q.Get("error_description"))
	}

	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), oauth2.HTTPClient, dexHTTP), requestTimeout)
	defer cancel()
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		t.Fatalf("dex token: %v", err)
	}
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		t.Fatal("dex token response has no id_token")
	}
	return idToken
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func jsonRequest(t *testing.T, method, rawURL, token string, body, out any) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode %s %s: %v", method, rawURL, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer drain(resp)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, rawURL, err)
	}
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, rawURL, data, err)
		}
	}
	return resp.StatusCode, data
}

type apiClient struct {
	token string
}

func (c apiClient) do(t *testing.T, method, path string, body, out any) (int, []byte) {
	t.Helper()
	return jsonRequest(t, method, apiBaseURL+path, c.token, body, out)
}

func (c apiClient) profiles(t *testing.T) ([]wire.Profile, int) {
	t.Helper()
	var out []wire.Profile
	code, _ := c.do(t, http.MethodGet, "/profiles", nil, &out)
	return out, code
}

func (c apiClient) createRun(t *testing.T, req wire.CreateRunRequest) (wire.Run, int) {
	t.Helper()
	var out wire.Run
	code, _ := c.do(t, http.MethodPost, "/runs", req, &out)
	return out, code
}

func (c apiClient) run(t *testing.T, name string) (wire.Run, int) {
	t.Helper()
	var out wire.Run
	code, _ := c.do(t, http.MethodGet, "/runs/"+url.PathEscape(name), nil, &out)
	return out, code
}

func (c apiClient) abort(t *testing.T, name string) int {
	t.Helper()
	code, _ := c.do(t, http.MethodPost, "/runs/"+url.PathEscape(name)+"/abort", nil, nil)
	return code
}

type eventStream struct {
	body io.ReadCloser
	mu   sync.Mutex
	pull func() (wire.Message, error, bool)
	stop func()
}

func newEventStream(body io.ReadCloser) *eventStream {
	pull, stop := iter.Pull2(wire.ReadEvents(body))
	return &eventStream{body: body, pull: pull, stop: stop}
}

func (c apiClient) openEvents(t *testing.T, ctx context.Context, name string, lastID int64) *eventStream {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/runs/"+url.PathEscape(name)+"/events", nil)
	if err != nil {
		t.Fatalf("events %s: %v", name, err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events %s: %v", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("events %s: status %d: %s", name, resp.StatusCode, b)
	}
	return newEventStream(resp.Body)
}

func (s *eventStream) next() (wire.LogEvent, error) {
	s.mu.Lock()
	m, err, ok := s.pull()
	s.mu.Unlock()
	if !ok {
		return wire.LogEvent{}, io.EOF
	}
	if err != nil {
		return wire.LogEvent{}, err
	}
	id, err := strconv.ParseInt(m.ID, 10, 64)
	if err != nil {
		return wire.LogEvent{}, fmt.Errorf("event %q has id %q: %w", m.Type, m.ID, err)
	}
	return wire.LogEvent{ID: id, Event: wire.Event{Type: m.Type, Data: m.Data}}, nil
}

func (s *eventStream) close() {
	_ = s.body.Close()
	s.mu.Lock()
	s.stop()
	s.mu.Unlock()
}

func events(t *testing.T, c apiClient, run string) []wire.LogEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventsTimeout)
	defer cancel()
	s := c.openEvents(t, ctx, run, 0)
	defer s.close()
	var out []wire.LogEvent
	for {
		ev, err := s.next()
		if err != nil {
			t.Fatalf("events %s: after %d events: %v", run, len(out), err)
		}
		out = append(out, ev)
		if ev.Type == wire.EventEnd {
			return out
		}
	}
}

type runOpts struct {
	Workspace *wire.Workspace
	Schema    json.RawMessage
}

func startRun(t *testing.T, c apiClient, profile, prompt string, opts runOpts) wire.Run {
	t.Helper()
	req := wire.CreateRunRequest{
		Profile:   profile,
		ClientKey: t.Name() + "-" + randomHex(t, 6),
		Workspace: opts.Workspace,
		Task:      wire.Task{Prompt: prompt, Schema: opts.Schema},
	}
	run, code := c.createRun(t, req)
	if code != http.StatusCreated {
		t.Fatalf("create run on %s: status %d", profile, code)
	}
	t.Cleanup(func() {
		var ar v1alpha1.AgentRun
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: runNamespace, Name: run.Name}, &ar); err != nil {
			return
		}
		if !ar.Status.Phase.Terminal() {
			c.abort(t, run.Name)
		}
	})
	return run
}

func waitPhase(t *testing.T, run string, phase v1alpha1.RunPhase, reason string, timeout time.Duration) *v1alpha1.AgentRun {
	t.Helper()
	var ar v1alpha1.AgentRun
	eventually(t, "run "+run+" reaches "+string(phase)+"/"+reason, timeout, func(ctx context.Context) (bool, string) {
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: runNamespace, Name: run}, &ar); err != nil {
			return false, err.Error()
		}
		st := ar.Status
		if st.Phase == phase && (reason == "" || st.Reason == reason) {
			return true, ""
		}
		if st.Phase.Terminal() {
			t.Fatalf("run %s ended %s/%s (%s), want %s/%s", run, st.Phase, st.Reason, st.Message, phase, reason)
		}
		return false, fmt.Sprintf("phase %q reason %q", st.Phase, st.Reason)
	})
	return &ar
}

func podOf(t *testing.T, run string) *corev1.Pod {
	t.Helper()
	var pod corev1.Pod
	eventually(t, "pod of run "+run, podTimeout, func(ctx context.Context) (bool, string) {
		var ar v1alpha1.AgentRun
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: runNamespace, Name: run}, &ar); err != nil {
			return false, err.Error()
		}
		if ar.Status.PodName == "" {
			return false, "status.podName is empty"
		}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: sandboxNamespace, Name: ar.Status.PodName}, &pod); err != nil {
			return false, err.Error()
		}
		if pod.UID != ar.Status.PodUID {
			return false, fmt.Sprintf("pod %s has uid %s, status.podUID is %s", pod.Name, pod.UID, ar.Status.PodUID)
		}
		return true, ""
	})
	return &pod
}

func kubectl(args ...string) *exec.Cmd {
	return exec.Command("kubectl", append([]string{"--context", kubeContext}, args...)...)
}

func execIn(t *testing.T, pod *corev1.Pod, cmd ...string) (string, error) {
	t.Helper()
	args := append([]string{"-n", pod.Namespace, "exec", pod.Name, "-c", sandbox.AgentContainerName, "--"}, cmd...)
	var stdout, stderr bytes.Buffer
	c := kubectl(args...)
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("kubectl exec %s: %v", pod.Name, err)
		}
		return stdout.String(), fmt.Errorf("exec %q in %s: %w: %s", cmd, pod.Name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

type litellmAdmin struct {
	masterKey string
}

func newLitellmAdmin(t *testing.T) litellmAdmin {
	t.Helper()
	return litellmAdmin{masterKey: secretValue(t, "gateway", "litellm-master", "masterKey")}
}

func (a litellmAdmin) do(t *testing.T, method, path string, body, out any) (int, []byte) {
	t.Helper()
	return litellmRequest(t, a.masterKey, method, path, body, out)
}

func litellmRequest(t *testing.T, key, method, path string, body, out any) (int, []byte) {
	t.Helper()
	return jsonRequest(t, method, litellmBaseURL+path, key, body, out)
}

// waitKeyRevoked proves the run key is deleted by a chat call: LiteLLM keeps answering /key/info from its cache after /key/delete.
func waitKeyRevoked(t *testing.T, run, key string, timeout time.Duration) {
	t.Helper()
	body := map[string]any{
		"model":    "e2e-primary",
		"messages": []map[string]string{{"role": "user", "content": "Reply with ok."}},
	}
	eventually(t, "run key of "+run+" revoked", timeout, func(context.Context) (bool, string) {
		code, resp := litellmRequest(t, key, http.MethodPost, "/v1/chat/completions", body, nil)
		if code == http.StatusUnauthorized && bytes.Contains(resp, []byte("token_not_found_in_db")) {
			return true, ""
		}
		return false, fmt.Sprintf("chat with the run key: status %d: %s", code, resp)
	})
}

type forgeClient struct {
	baseURL string
}

type forgePull struct {
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
		SHA   string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func newForgeClient(t *testing.T) forgeClient {
	t.Helper()
	return forgeClient{baseURL: portForward(t, "skua-e2e", "deployment/skua-e2e-forge", 8080)}
}

func (f forgeClient) do(t *testing.T, method, path, token string, body, out any) (int, []byte) {
	t.Helper()
	return jsonRequest(t, method, f.baseURL+path, token, body, out)
}

func (f forgeClient) get(t *testing.T, path string, out any) {
	t.Helper()
	if code, body := f.do(t, http.MethodGet, path, "", nil, out); code != http.StatusOK {
		t.Fatalf("forge GET %s: status %d: %s", path, code, body)
	}
}

func (f forgeClient) refs(t *testing.T, repo string) map[string]string {
	t.Helper()
	out := map[string]string{}
	f.get(t, "/_test/refs/"+repo, &out)
	return out
}

func (f forgeClient) pulls(t *testing.T, repo string) []forgePull {
	t.Helper()
	var out []forgePull
	f.get(t, "/_test/pulls/"+repo, &out)
	return out
}

func fakellmRequests(t *testing.T) []litellmtest.RequestRecord {
	t.Helper()
	base := portForward(t, "gateway", "deployment/fakellm", 8080)
	var out []litellmtest.RequestRecord
	if code, body := jsonRequest(t, http.MethodGet, base+"/_test/requests", "", nil, &out); code != http.StatusOK {
		t.Fatalf("fakellm requests: status %d: %s", code, body)
	}
	return out
}

func fakellmReset(t *testing.T) {
	t.Helper()
	base := portForward(t, "gateway", "deployment/fakellm", 8080)
	if code, body := jsonRequest(t, http.MethodPost, base+"/_test/reset", "", nil, nil); code != http.StatusNoContent {
		t.Fatalf("fakellm reset: status %d: %s", code, body)
	}
}

func tempProfile(t *testing.T, base string, mutate func(*v1alpha1.AgentProfileSpec)) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var src v1alpha1.AgentProfile
	if err := k8s.Get(ctx, client.ObjectKey{Name: base}, &src); err != nil {
		t.Fatalf("get profile %s: %v", base, err)
	}
	p := &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: base + "-" + randomHex(t, 3)},
		Spec:       *src.Spec.DeepCopy(),
	}
	mutate(&p.Spec)
	if err := k8s.Create(ctx, p); err != nil {
		t.Fatalf("create profile %s: %v", p.Name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := k8s.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete profile %s: %v", p.Name, err)
		}
	})
	eventually(t, "profile "+p.Name+" Ready", profileTimeout, func(ctx context.Context) (bool, string) {
		var cur v1alpha1.AgentProfile
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(p), &cur); err != nil {
			return false, err.Error()
		}
		cond := meta.FindStatusCondition(cur.Status.Conditions, conditionReady)
		if cond == nil {
			return false, "no Ready condition"
		}
		if cond.Status != metav1.ConditionTrue {
			return false, fmt.Sprintf("Ready=%s: %s: %s", cond.Status, cond.Reason, cond.Message)
		}
		return true, ""
	})
	return p.Name
}

var (
	forwardsMu sync.Mutex
	forwards   = map[string]*portForwarder{}
	forwardRE  = regexp.MustCompile(`^Forwarding from 127\.0\.0\.1:(\d+) ->`)
)

type portForwarder struct {
	cmd  *exec.Cmd
	url  string
	done chan struct{}
}

func portForward(t *testing.T, namespace, target string, port int) string {
	t.Helper()
	forwardsMu.Lock()
	defer forwardsMu.Unlock()
	key := namespace + "/" + target + ":" + strconv.Itoa(port)
	if pf, ok := forwards[key]; ok {
		select {
		case <-pf.done:
		default:
			return pf.url
		}
	}
	pf, err := startPortForward(namespace, target, port)
	if err != nil {
		t.Fatalf("port-forward %s: %v", key, err)
	}
	forwards[key] = pf
	return pf.url
}

func startPortForward(namespace, target string, port int) (*portForwarder, error) {
	cmd := kubectl("-n", namespace, "port-forward", "--address", "127.0.0.1", target, ":"+strconv.Itoa(port))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pf := &portForwarder{cmd: cmd, done: make(chan struct{})}
	local := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if m := forwardRE.FindStringSubmatch(sc.Text()); m != nil {
				select {
				case local <- m[1]:
				default:
				}
			}
		}
		_ = cmd.Wait()
		close(pf.done)
	}()
	select {
	case p := <-local:
		pf.url = "http://127.0.0.1:" + p
	case <-pf.done:
		return nil, fmt.Errorf("kubectl exited: %s", strings.TrimSpace(stderr.String()))
	case <-time.After(requestTimeout):
		pf.stop()
		return nil, errors.New("no forwarding port within " + requestTimeout.String())
	}
	return pf, nil
}

func (pf *portForwarder) stop() {
	_ = pf.cmd.Process.Kill()
	<-pf.done
}

func stopPortForwards() {
	forwardsMu.Lock()
	defer forwardsMu.Unlock()
	for key, pf := range forwards {
		pf.stop()
		delete(forwards, key)
	}
}
