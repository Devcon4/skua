//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

const (
	s28Profile        = "fake"
	s28User           = "dev@skua.test"
	s28ForgeRepo      = "e2e/sample"
	s28OtherRepo      = "e2e/other"
	s28BaseRef        = "main"
	s28TokenPath      = sandbox.TokenDir + "/" + sandbox.TokenFile
	s28ForgeTokenPath = sandbox.RunDir + "/forge-token.json"
	s28KeyMarker      = "BEGIN RSA PRIVATE KEY"
	s28AppSecretNS    = "flux-system"
	s28AppSecret      = "skua-test-github-app"
	s28DecoySecret    = "other-secret"
	s28OperatorUser   = "system:serviceaccount:" + runNamespace + ":skua-operator"
	s28CurlTimedOut   = 28
	s28ListenPort     = 8080
	s28RunTimeout     = 5 * time.Minute
)

const s28RunnerPostScript = `curl -s -m 10 -w '\n%{http_code}' -X POST ` +
	`-H "Authorization: Bearer $(cat ` + s28TokenPath + `)" ` +
	`-H "` + wire.HeaderRunnerVersion + `: $2" ` +
	`-H 'Content-Type: application/json' --data-binary "$3" "$` + sandbox.EnvAPIURL + `/v1alpha1$1"`

const s28PushScript = `cd ` + sandbox.WorkDir + `/repo && ` +
	`export PATH=` + sandbox.BinDir + `:$PATH SKUA_FORGE_TOKEN_FILE=` + s28ForgeTokenPath + ` && ` +
	`GIT_TERMINAL_PROMPT=0 git -c credential.helper=skua push "$` + sandbox.EnvForgeGitURL + `/$1.git" "HEAD:refs/heads/$2" 2>&1`

const s28ScanDirs = sandbox.WorkDir + " /skua /var/run/secrets"

const s28ScanScript = `find ` + s28ScanDirs + ` -type f -exec grep -l -- "` + s28KeyMarker + `" {} + 2>/dev/null; ` +
	`echo "scanned=$(find ` + s28ScanDirs + ` -type f 2>/dev/null | wc -l)"`

func TestRunnerTokenReview(t *testing.T) {
	t.Parallel()
	profile := tempProfile(t, s28Profile, func(s *v1alpha1.AgentProfileSpec) {
		s.WarmPool.Replicas = ptr.To[int32](1)
	})
	warm := s28WarmPod(t, profile, "")
	version := s28RunnerVersion(t, warm)

	other := s28CreateToken(t, warm, "other")
	code, body := s28StreamStatus(t, other, version)
	s28WantRefusal(t, "token with audience other", code, body, s28Refusal{http.StatusUnauthorized, wire.RefusalTokenInvalid})

	own := s28PodToken(t, warm)
	eventually(t, "own token of "+warm.Name+" refused while its runner holds the stream", podTimeout, func(context.Context) (bool, string) {
		code, body := s28StreamStatus(t, own, version)
		if code == http.StatusConflict {
			return true, ""
		}
		return false, fmt.Sprintf("status %d: %s", code, body)
	})

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := clientset.CoreV1().Pods(warm.Namespace).Delete(ctx, warm.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete warm pod %s: %v", warm.Name, err)
	}
	s28WaitPodGone(t, warm)
	code, body = s28StreamStatus(t, own, version)
	s28WantRefusal(t, "token of deleted pod "+warm.Name, code, body,
		s28Refusal{http.StatusUnauthorized, wire.RefusalTokenInvalid}, s28Refusal{http.StatusForbidden, wire.RefusalPodMismatch})

	live := s28WarmPod(t, profile, warm.UID)
	code, body = s28StreamStatus(t, s28PodToken(t, live), "0.0.1")
	s28WantRefusal(t, "runner version 0.0.1", code, body, s28Refusal{http.StatusBadRequest, v1alpha1.ReasonRunnerUnsupported})
}

func TestSandboxNetworkPolicy(t *testing.T) {
	t.Parallel()
	external := s28EnvFile(t)["SKUA_EXTERNAL_URL"]
	if external == "" {
		t.Fatal("SKUA_EXTERNAL_URL is not set in the kind env file")
	}
	c := apiClient{token: login(t, s28User)}
	run := startRun(t, c, s28Profile, "SCRIPT:sleep 300", runOpts{})
	waitPhase(t, run.Name, v1alpha1.PhaseRunning, "", s28RunTimeout)
	pod := podOf(t, run.Name)
	if pod.Status.HostIP == "" || pod.Status.PodIP == "" {
		t.Fatalf("pod %s has host IP %q and pod IP %q", pod.Name, pod.Status.HostIP, pod.Status.PodIP)
	}

	eventually(t, "control: fakeomp --listen answers inside "+pod.Name, podTimeout, func(context.Context) (bool, string) {
		body, code, exit := s28Curl(t, pod, fmt.Sprintf("http://127.0.0.1:%d", s28ListenPort))
		if exit == 0 && code == http.StatusOK && body == "ok" {
			return true, ""
		}
		return false, fmt.Sprintf("curl exit %d status %d body %q", exit, code, body)
	})

	for _, tc := range []struct {
		url  string
		code int
		body string
	}{
		{url: external, code: http.StatusOK, body: "external"},
		{url: "http://litellm.gateway.svc:4000/health/liveliness", code: http.StatusOK},
		{url: "http://skua-api.skua.svc/v1alpha1/profiles", code: http.StatusUnauthorized},
	} {
		body, code, exit := s28Curl(t, pod, tc.url)
		if exit != 0 || code != tc.code || (tc.body != "" && body != tc.body) {
			t.Errorf("curl %s from %s: exit %d status %d body %q, want status %d body %q", tc.url, pod.Name, exit, code, body, tc.code, tc.body)
		}
	}

	for _, url := range []string{
		"https://kubernetes.default.svc",
		"http://dex.dex.svc:32000",
		"https://" + pod.Status.HostIP + ":6443",
		"https://" + pod.Status.HostIP + ":10250",
		"http://169.254.169.254",
	} {
		if body, code, exit := s28Curl(t, pod, url); exit != s28CurlTimedOut {
			t.Errorf("curl %s from %s: exit %d status %d body %q, want a timeout (exit %d)", url, pod.Name, exit, code, body, s28CurlTimedOut)
		}
	}

	prober := s28CurlPod(t)
	target := fmt.Sprintf("http://%s:%d", pod.Status.PodIP, s28ListenPort)
	if body, code, exit := s28Curl(t, prober, target); exit != s28CurlTimedOut {
		t.Errorf("curl %s from %s/%s: exit %d status %d body %q, want a timeout (exit %d)", target, prober.Namespace, prober.Name, exit, code, body, s28CurlTimedOut)
	}
}

func TestRunKeyCannotManageKeys(t *testing.T) {
	t.Parallel()
	c := apiClient{token: login(t, s28User)}
	run := startRun(t, c, s28Profile, "SCRIPT:sleep 120", runOpts{})
	waitPhase(t, run.Name, v1alpha1.PhaseRunning, "", s28RunTimeout)
	key := secretValue(t, runNamespace, "skua-run-"+run.Name, "litellmKey")

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{method: http.MethodPost, path: "/key/generate", body: map[string]any{"key_alias": "s28-" + run.Name}},
		{method: http.MethodPost, path: "/key/delete", body: map[string]any{"keys": []string{key}}},
		{method: http.MethodGet, path: "/key/info?key=" + key},
	} {
		if code, body := litellmRequest(t, key, tc.method, tc.path, tc.body, nil); code != http.StatusForbidden {
			t.Errorf("%s %s with the run key: status %d, want 403: %s", tc.method, tc.path, code, body)
		}
	}
}

func TestAgentRootFilesystemReadOnly(t *testing.T) {
	t.Parallel()
	c := apiClient{token: login(t, s28User)}
	run := startRun(t, c, s28Profile, "SCRIPT:sleep 120", runOpts{})
	waitPhase(t, run.Name, v1alpha1.PhaseRunning, "", s28RunTimeout)
	pod := podOf(t, run.Name)
	if out, err := execIn(t, pod, "touch", "/usr/s28-probe"); err == nil {
		t.Errorf("touch /usr/s28-probe succeeded, want a read-only root filesystem: %s", out)
	}
	for _, dir := range []string{"/tmp", sandbox.WorkDir, sandbox.RunDir} {
		if out, err := execIn(t, pod, "touch", dir+"/s28-probe"); err != nil {
			t.Errorf("touch %s/s28-probe: %v: %s", dir, err, out)
		}
	}
}

func TestRunnerCannotPostForOtherRun(t *testing.T) {
	t.Parallel()
	c := apiClient{token: login(t, s28User)}
	runA := startRun(t, c, s28Profile, "SCRIPT:sleep 300", runOpts{})
	runB := startRun(t, c, s28Profile, "SCRIPT:sleep 300", runOpts{})
	waitPhase(t, runA.Name, v1alpha1.PhaseRunning, "", s28RunTimeout)
	before := waitPhase(t, runB.Name, v1alpha1.PhaseRunning, "", s28RunTimeout).Status
	podA := podOf(t, runA.Name)
	version := s28RunnerVersion(t, podA)

	marker := "s28-injected-" + randomHex(t, 4)
	for _, tc := range []struct {
		path string
		body any
	}{
		{path: "/runner/events", body: wire.RunnerEventsRequest{Run: runB.Name, Events: []wire.RunnerEvent{
			{Seq: 1 << 40, Type: wire.EventText, Data: s28JSON(t, map[string]string{"delta": marker})},
		}}},
		{path: "/runner/result", body: wire.RunnerResult{Run: runB.Name, Phase: string(v1alpha1.PhaseSucceeded), Reason: v1alpha1.ReasonCompleted, Result: s28JSON(t, marker)}},
	} {
		code, body := s28InPodRunnerPost(t, podA, tc.path, version, tc.body)
		if code != http.StatusForbidden {
			t.Errorf("POST %s naming %s from %s: status %d, want 403: %s", tc.path, runB.Name, podA.Name, code, body)
		}
	}

	var after v1alpha1.AgentRun
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: runNamespace, Name: runB.Name}, &after); err != nil {
		t.Fatalf("get run %s: %v", runB.Name, err)
	}
	if st := after.Status; st.Phase != before.Phase || st.Reason != before.Reason || st.Result != nil || st.PodUID != before.PodUID {
		t.Fatalf("run %s changed: phase %s reason %q result %v pod %s, was %s %q pod %s", runB.Name, st.Phase, st.Reason, st.Result, st.PodUID, before.Phase, before.Reason, before.PodUID)
	}

	if code := c.abort(t, runB.Name); code != http.StatusAccepted {
		t.Fatalf("abort %s: status %d", runB.Name, code)
	}
	for _, ev := range events(t, c, runB.Name) {
		if strings.Contains(string(ev.Data), marker) {
			t.Errorf("run %s event %d %s carries the injected marker: %s", runB.Name, ev.ID, ev.Type, ev.Data)
		}
	}
}

func TestAppSecretNeverInSandbox(t *testing.T) {
	t.Parallel()
	c := apiClient{token: login(t, s28User)}
	run := s28ForgeRun(t, c, "SCRIPT:sleep 300")
	waitPhase(t, run.Name, v1alpha1.PhaseRunning, "", s28RunTimeout)
	pod := podOf(t, run.Name)
	eventually(t, "forge token delivered to "+pod.Name, podTimeout, func(context.Context) (bool, string) {
		if _, err := execIn(t, pod, "test", "-s", s28ForgeTokenPath); err != nil {
			return false, err.Error()
		}
		return true, ""
	})

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	pods, err := clientset.CoreV1().Pods(sandboxNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods in %s: %v", sandboxNamespace, err)
	}
	if !slices.ContainsFunc(pods.Items, func(p corev1.Pod) bool { return p.UID == pod.UID }) {
		t.Fatalf("pod %s missing from the %s listing", pod.Name, sandboxNamespace)
	}
	for i := range pods.Items {
		if refs := s28SecretRefs(&pods.Items[i]); len(refs) > 0 {
			t.Errorf("pod %s references Secrets: %s", pods.Items[i].Name, strings.Join(refs, ", "))
		}
	}

	out, err := execIn(t, pod, "sh", "-c", s28ScanScript)
	if err != nil {
		t.Fatalf("scan %s: %v", pod.Name, err)
	}
	var hits []string
	scanned := 0
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if n, ok := strings.CutPrefix(line, "scanned="); ok {
			if scanned, err = strconv.Atoi(n); err != nil {
				t.Fatalf("scan %s: count %q: %v", pod.Name, n, err)
			}
			continue
		}
		if line != "" {
			hits = append(hits, line)
		}
	}
	if scanned == 0 {
		t.Fatalf("scan of %s found no files: %q", pod.Name, out)
	}
	if len(hits) > 0 {
		t.Errorf("files in %s contain %q: %s", pod.Name, s28KeyMarker, strings.Join(hits, ", "))
	}
}

func TestAppSecretRBAC(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		verb, resource string
		want           string
	}{
		{verb: "get", resource: "secret/" + s28AppSecret, want: "yes"},
		{verb: "list", resource: "secrets", want: "no"},
		{verb: "watch", resource: "secrets", want: "no"},
		{verb: "get", resource: "secret/" + s28DecoySecret, want: "no"},
	} {
		out, _ := s28Kubectl(t, "auth", "can-i", tc.verb, tc.resource, "-n", s28AppSecretNS, "--as="+s28OperatorUser)
		if got := strings.TrimSpace(out); got != tc.want {
			t.Errorf("can-i %s %s -n %s as skua-operator = %q, want %q", tc.verb, tc.resource, s28AppSecretNS, got, tc.want)
		}
	}

	logs, err := s28Kubectl(t, "-n", runNamespace, "logs", "deployment/skua-operator", "--all-containers=true")
	if err != nil {
		t.Fatalf("operator logs: %v", err)
	}
	if strings.TrimSpace(logs) == "" {
		t.Fatal("operator logs are empty")
	}
	for line := range strings.Lines(logs) {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, s28AppSecretNS) || !strings.Contains(lower, "secret") {
			continue
		}
		for _, word := range []string{"informer", "reflector", "eventsource", "watch", "list"} {
			if strings.Contains(lower, word) {
				t.Errorf("operator log starts a Secret informer for %s: %s", s28AppSecretNS, strings.TrimSpace(line))
				break
			}
		}
	}
}

func TestPushOutsideSkuaRefused(t *testing.T) {
	t.Parallel()
	c := apiClient{token: login(t, s28User)}
	forge := newForgeClient(t)
	mainRef := "refs/heads/" + s28BaseRef
	mainBefore := forge.refs(t, s28ForgeRepo)[mainRef]
	if mainBefore == "" {
		t.Fatalf("forge %s has no %s", s28ForgeRepo, mainRef)
	}

	pushMain := s28ForgeRun(t, c, "SCRIPT:push-main")
	ar := waitPhase(t, pushMain.Name, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted, s28RunTimeout)
	result := s28ResultText(t, ar)
	exit, ok := strings.CutPrefix(result, "push-main: ")
	if !ok || exit == "0" {
		t.Errorf("run %s result %q, want a non-zero push-main exit code", pushMain.Name, result)
	}
	if got := forge.refs(t, s28ForgeRepo)[mainRef]; got != mainBefore {
		t.Errorf("forge %s %s moved from %s to %s", s28ForgeRepo, mainRef, mainBefore, got)
	}

	run := s28ForgeRun(t, c, "SCRIPT:sleep 300")
	waitPhase(t, run.Name, v1alpha1.PhaseRunning, "", s28RunTimeout)
	pod := podOf(t, run.Name)
	branch := "skua/" + run.Name
	eventually(t, "control: run token pushes "+branch+" to "+s28ForgeRepo, podTimeout, func(context.Context) (bool, string) {
		out, err := execIn(t, pod, "sh", "-c", s28PushScript, "s28", s28ForgeRepo, branch)
		if err != nil {
			return false, fmt.Sprintf("%v: %s", err, strings.TrimSpace(out))
		}
		return true, ""
	})
	if _, ok := forge.refs(t, s28ForgeRepo)["refs/heads/"+branch]; !ok {
		t.Fatalf("control push: forge %s has no %s", s28ForgeRepo, branch)
	}

	out, err := execIn(t, pod, "sh", "-c", s28PushScript, "s28", s28OtherRepo, branch)
	if err == nil {
		t.Errorf("push of %s to %s with the run token succeeded: %s", branch, s28OtherRepo, strings.TrimSpace(out))
	}
	if sha, ok := forge.refs(t, s28OtherRepo)["refs/heads/"+branch]; ok {
		t.Errorf("forge %s has %s at %s", s28OtherRepo, branch, sha)
	}
}

func s28ForgeRun(t *testing.T, c apiClient, prompt string) wire.Run {
	t.Helper()
	return startRun(t, c, s28Profile, prompt, runOpts{Workspace: &wire.Workspace{Repo: s28ForgeRepo, BaseRef: s28BaseRef}})
}

func s28WarmPod(t *testing.T, profile string, exclude types.UID) *corev1.Pod {
	t.Helper()
	var warm corev1.Pod
	eventually(t, "warm pod of "+profile, podTimeout, func(ctx context.Context) (bool, string) {
		var pods corev1.PodList
		if err := k8s.List(ctx, &pods, client.InNamespace(sandboxNamespace), client.MatchingLabels{sandbox.LabelProfile: profile}); err != nil {
			return false, err.Error()
		}
		for _, p := range pods.Items {
			if p.UID == exclude || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
				continue
			}
			if _, claimed := p.Labels[extv1beta1.SandboxIDLabel]; claimed {
				continue
			}
			if slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool {
				return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
			}) {
				warm = p
				return true, ""
			}
		}
		return false, fmt.Sprintf("no unclaimed Ready pod among %d", len(pods.Items))
	})
	return &warm
}

func s28WaitPodGone(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	eventually(t, "pod "+pod.Name+" deleted", podTimeout, func(ctx context.Context) (bool, string) {
		var cur corev1.Pod
		err := k8s.Get(ctx, client.ObjectKeyFromObject(pod), &cur)
		switch {
		case apierrors.IsNotFound(err):
			return true, ""
		case err != nil:
			return false, err.Error()
		case cur.UID != pod.UID:
			return true, ""
		}
		return false, "pod still exists"
	})
}

func s28PodToken(t *testing.T, pod *corev1.Pod) string {
	t.Helper()
	out, err := execIn(t, pod, "cat", s28TokenPath)
	if err != nil {
		t.Fatalf("read token in %s: %v", pod.Name, err)
	}
	return strings.TrimSpace(out)
}

func s28RunnerVersion(t *testing.T, pod *corev1.Pod) string {
	t.Helper()
	out, err := execIn(t, pod, sandbox.BinDir+"/skua", "version")
	if err != nil {
		t.Fatalf("runner version in %s: %v", pod.Name, err)
	}
	return strings.TrimSpace(out)
}

func s28CreateToken(t *testing.T, pod *corev1.Pod, audience string) string {
	t.Helper()
	out, err := s28Kubectl(t, "create", "token", sandbox.ServiceAccountName, "-n", pod.Namespace, "--audience", audience,
		"--bound-object-kind", "Pod", "--bound-object-name", pod.Name, "--bound-object-uid", string(pod.UID))
	if err != nil {
		t.Fatalf("create token for %s: %v", pod.Name, err)
	}
	return strings.TrimSpace(out)
}

func s28StreamStatus(t *testing.T, token, version string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/runner/stream", nil)
	if err != nil {
		t.Fatalf("runner stream request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(wire.HeaderRunnerVersion, version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /runner/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return resp.StatusCode, "stream opened"
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, strings.TrimSpace(string(body))
}

type s28Refusal struct {
	code   int
	reason string
}

func s28WantRefusal(t *testing.T, what string, code int, body string, want ...s28Refusal) {
	t.Helper()
	for _, w := range want {
		if code == w.code && strings.HasPrefix(body, w.reason+":") {
			return
		}
	}
	t.Fatalf("%s: status %d body %q, want one of %+v", what, code, body, want)
}

func s28InPodRunnerPost(t *testing.T, pod *corev1.Pod, path, version string, body any) (int, string) {
	t.Helper()
	out, err := execIn(t, pod, "sh", "-c", s28RunnerPostScript, "s28", path, version, string(s28JSON(t, body)))
	if err != nil {
		t.Fatalf("POST %s from %s: %v", path, pod.Name, err)
	}
	resp, code := s28SplitStatus(t, out)
	return code, resp
}

func s28Curl(t *testing.T, pod *corev1.Pod, url string) (body string, code, exit int) {
	t.Helper()
	out, err := execIn(t, pod, "curl", "-s", "-k", "-m", "5", "-w", `\n%{http_code}`, url)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("curl %s in %s: %v", url, pod.Name, err)
		}
		exit = exitErr.ExitCode()
	}
	if exit != 0 && !strings.Contains(out, "\n") {
		return strings.TrimSpace(out), 0, exit
	}
	body, code = s28SplitStatus(t, out)
	return body, code, exit
}

func s28SplitStatus(t *testing.T, out string) (string, int) {
	t.Helper()
	i := strings.LastIndexByte(out, '\n')
	if i < 0 {
		t.Fatalf("curl output %q has no status line", out)
	}
	code, err := strconv.Atoi(strings.TrimSpace(out[i+1:]))
	if err != nil {
		t.Fatalf("curl status %q: %v", out[i+1:], err)
	}
	return strings.TrimSpace(out[:i]), code
}

func s28CurlPod(t *testing.T) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "s28-curl-" + randomHex(t, 3), Namespace: "skua-e2e"},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: ptr.To[int64](0),
			AutomountServiceAccountToken:  ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            sandbox.AgentContainerName,
				Image:           "skua-e2e-fakeagent:dev",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sleep", "3600"},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := k8s.Create(ctx, pod); err != nil {
		t.Fatalf("create curl pod: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := k8s.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete curl pod %s: %v", pod.Name, err)
		}
	})
	eventually(t, "curl pod "+pod.Name+" running", podTimeout, func(ctx context.Context) (bool, string) {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return false, err.Error()
		}
		return pod.Status.Phase == corev1.PodRunning, "phase " + string(pod.Status.Phase)
	})
	return pod
}

func s28EnvFile(t *testing.T) map[string]string {
	t.Helper()
	path := "/tmp/" + strings.TrimPrefix(kubeContext, "kind-") + ".env"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("kind env file: %v", err)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			out[k] = strings.Trim(v, `"'`)
		}
	}
	return out
}

func s28SecretRefs(pod *corev1.Pod) []string {
	var refs []string
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil {
			refs = append(refs, "volume "+v.Name+" secret "+v.Secret.SecretName)
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.Secret != nil {
					refs = append(refs, "volume "+v.Name+" projected secret "+src.Secret.Name)
				}
			}
		}
	}
	var containers []corev1.Container
	containers = append(containers, pod.Spec.InitContainers...)
	containers = append(containers, pod.Spec.Containers...)
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				refs = append(refs, "container "+c.Name+" env "+e.Name+" secret "+e.ValueFrom.SecretKeyRef.Name)
			}
		}
		for _, e := range c.EnvFrom {
			if e.SecretRef != nil {
				refs = append(refs, "container "+c.Name+" envFrom secret "+e.SecretRef.Name)
			}
		}
	}
	for _, c := range pod.Spec.EphemeralContainers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				refs = append(refs, "ephemeral container "+c.Name+" env "+e.Name+" secret "+e.ValueFrom.SecretKeyRef.Name)
			}
		}
		for _, e := range c.EnvFrom {
			if e.SecretRef != nil {
				refs = append(refs, "ephemeral container "+c.Name+" envFrom secret "+e.SecretRef.Name)
			}
		}
	}
	return refs
}

func s28ResultText(t *testing.T, ar *v1alpha1.AgentRun) string {
	t.Helper()
	if ar.Status.Result == nil {
		t.Fatalf("run %s has no result", ar.Name)
	}
	var s string
	if err := json.Unmarshal(ar.Status.Result.Raw, &s); err != nil {
		t.Fatalf("run %s result %s is not a string: %v", ar.Name, ar.Status.Result.Raw, err)
	}
	return s
}

func s28Kubectl(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := kubectl(args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("kubectl %s: %v", strings.Join(args, " "), err)
		}
		return stdout.String(), fmt.Errorf("kubectl %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func s28JSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return b
}
