package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/integration/github"
	"github.com/Devcon4/skua/internal/integration/github/githubtest"
	"github.com/Devcon4/skua/internal/runner/gitcred"
)

const forgeKeyTokensLive = "forgeTokensLive"

var (
	stepRE         = regexp.MustCompile(`^step (\d+)$`)
	storageLimitRE = regexp.MustCompile(`(?i)ephemeral|emptydir`)
	restartsRE     = regexp.MustCompile(`agent container restarted \d+ times`)
)

func TestPodLost(t *testing.T) {
	s := newStack(t, stackOptions{})
	ar, pod := s.running("SCRIPT:steps 100 2s")
	s.binder.LosePod(pod)
	s.waitPhase(ar.Name, v1alpha1.PhaseFailed, v1alpha1.ReasonPodLost)
	s.waitGone("claim "+ar.Name, s.claimKey(ar.Name), &extv1beta1.SandboxClaim{})
}

func TestWorkspaceOverrun(t *testing.T) {
	s := newStack(t, stackOptions{profile: func(sp *v1alpha1.AgentProfileSpec) {
		sp.Workspace.Size = resource.MustParse("1Gi")
	}})
	ar, pod := s.running("SCRIPT:sleep 600")
	s.binder.Evict(pod, fmt.Sprintf("Usage of EmptyDir volume %q exceeds the limit %q. ", sandbox.VolumeWorkspace, "1Gi"))
	got := s.waitPhase(ar.Name, v1alpha1.PhaseFailed, v1alpha1.ReasonPodLost)
	if !storageLimitRE.MatchString(got.Status.Message) {
		t.Errorf("message = %q, want it to name the ephemeral-storage or EmptyDir limit", got.Status.Message)
	}
}

func TestMaxRuntime(t *testing.T) {
	const maxRuntime = 2 * time.Minute
	s := newStack(t, stackOptions{warmPods: 2, profile: func(sp *v1alpha1.AgentProfileSpec) {
		sp.Limits.MaxRuntime = metav1.Duration{Duration: maxRuntime}
	}})
	warm := s.podUIDs()
	ar, pod := s.running("SCRIPT:sleep 600")
	key := s.runKey(ar.Name)

	if deadline := ar.CreationTimestamp.Add(maxRuntime); s.clock.Now().Before(deadline) {
		s.clock.SetTime(deadline)
	}
	var got v1alpha1.AgentRun
	s.eventually("run "+ar.Name+" Failed/MaxRuntime", func(ctx context.Context) (bool, string) {
		s.nudge(ar.Name)
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(ar), &got); err != nil {
			return false, err.Error()
		}
		return got.Status.Phase == v1alpha1.PhaseFailed && got.Status.Reason == v1alpha1.ReasonMaxRuntime,
			fmt.Sprintf("phase %s/%s", got.Status.Phase, got.Status.Reason)
	})
	if got.Status.FinishedAt == nil {
		t.Fatalf("run %s is Failed/MaxRuntime without status.finishedAt", ar.Name)
	}
	if took := got.Status.FinishedAt.Sub(got.CreationTimestamp.Time); took > maxRuntime+30*time.Second {
		t.Errorf("run %s turned Failed/MaxRuntime %s after creation, want at most %s", ar.Name, took, maxRuntime+30*time.Second)
	}
	s.waitGone("claim "+ar.Name, s.claimKey(ar.Name), &extv1beta1.SandboxClaim{})
	s.waitGone("pod "+pod.Name, pod, &corev1.Pod{})

	alias := "skua-" + string(ar.UID)
	s.eventually("key "+alias+" revoked", func(context.Context) (bool, string) {
		k, ok := s.gateway.Keys()[alias]
		if !ok {
			return false, "no key with alias " + alias
		}
		if k.Value != key {
			return false, "key value is not the run Secret's"
		}
		return k.Revoked, fmt.Sprintf("revoked %t", k.Revoked)
	})

	var pool extv1beta1.SandboxWarmPool
	poolKey := client.ObjectKey{Namespace: s.cfg.SandboxNamespace, Name: sandbox.Name(s.profile.Name)}
	if err := env.Client.Get(t.Context(), poolKey, &pool); err != nil {
		t.Fatalf("get pool %s: %v", poolKey.Name, err)
	}
	if pool.Spec.Replicas == nil || *pool.Spec.Replicas != 2 {
		t.Errorf("pool %s spec.replicas = %v, want 2", poolKey.Name, pool.Spec.Replicas)
	}
	after := s.podUIDs()
	for name, uid := range warm {
		if name == pod.Name {
			continue
		}
		if after[name] != uid {
			t.Errorf("warm pod %s (uid %s) not kept after the run: now uid %q", name, uid, after[name])
		}
	}
}

func TestCrashLoop(t *testing.T) {
	maxRestarts := int32(2)
	s := newStack(t, stackOptions{profile: func(sp *v1alpha1.AgentProfileSpec) {
		sp.Limits.MaxRestarts = &maxRestarts
	}})
	run := s.startRun("SCRIPT:term-runner", nil)
	got := s.waitPhase(run.Name, v1alpha1.PhaseFailed, v1alpha1.ReasonCrashLoop)
	if !restartsRE.MatchString(got.Status.Message) {
		t.Errorf("message = %q, want the operator's count of agent container restarts", got.Status.Message)
	}
}

func TestRunnerContainerRestart(t *testing.T) {
	s := newStack(t, stackOptions{})
	run := s.startRun("SCRIPT:steps 12 200ms", nil)
	ctx, cancel := context.WithTimeout(t.Context(), waitTimeout)
	defer cancel()
	es, err := s.openEvents(ctx, run.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer es.close()
	var got []wire.LogEvent
	readUntil(t, es, &got, "text step 3", isText("step 3"))

	pod := s.podKey(s.waitPhase(run.Name, v1alpha1.PhaseRunning, ""))
	s.kubelet.stop(pod.Name)
	var restarts int32
	s.eventually("agent container of "+pod.Name+" restarted", func(ctx context.Context) (bool, string) {
		var cur corev1.Pod
		if err := env.Client.Get(ctx, pod, &cur); err != nil {
			return false, err.Error()
		}
		n, running := agentStatus(&cur)
		restarts = n
		return n >= 1 && running, fmt.Sprintf("restartCount %d, running %t", n, running)
	})
	if restarts != 1 {
		t.Errorf("agent container restartCount = %d, want 1", restarts)
	}

	readUntil(t, es, &got, "end", isEnd)
	s.waitPhase(run.Name, v1alpha1.PhaseSucceeded, "")
	want := make([]int, 12)
	for i := range want {
		want[i] = i + 1
	}
	steps := stepNumbers(got)
	if !slices.IsSorted(steps) || !slices.Equal(slices.Compact(slices.Clone(steps)), want) {
		t.Errorf("step text events = %v, want every step 1..12 in order; a step repeated after the restart is allowed", steps)
	}
}

func TestApiRestart(t *testing.T) {
	s := newStack(t, stackOptions{})
	run := s.startRun("SCRIPT:steps 30 100ms", nil)
	ctx, cancel := context.WithTimeout(t.Context(), waitTimeout)
	defer cancel()
	first, err := s.openEvents(ctx, run.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	var before []wire.LogEvent
	readUntil(t, first, &before, "text step 5", isText("step 5"))
	first.close()
	lastID := before[len(before)-1].ID

	s.restartAPI(30 * time.Second)

	var es *eventStream
	s.eventually("events stream of "+run.Name+" reconnects", func(context.Context) (bool, string) {
		var err error
		es, err = s.openEvents(ctx, run.Name, lastID)
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	})
	defer es.close()
	var after []wire.LogEvent
	readUntil(t, es, &after, "end", isEnd)
	s.waitPhase(run.Name, v1alpha1.PhaseSucceeded, "")

	var end wire.EndData
	if err := json.Unmarshal(after[len(after)-1].Data, &end); err != nil {
		t.Fatalf("decode end event %s: %v", after[len(after)-1].Data, err)
	}
	if end.Phase != string(v1alpha1.PhaseSucceeded) {
		t.Errorf("end event phase = %q, want %q", end.Phase, v1alpha1.PhaseSucceeded)
	}
	steps := stepNumbers(after)
	for i := 1; i < len(steps); i++ {
		if steps[i] <= steps[i-1] {
			t.Errorf("step text events after the reconnect = %v, want strictly increasing", steps)
			break
		}
	}
}

func TestSteerAndAbort(t *testing.T) {
	s := newStack(t, stackOptions{})
	run := s.startRun("SCRIPT:steps 30 1s", nil)
	ctx, cancel := context.WithTimeout(t.Context(), waitTimeout)
	defer cancel()
	evs := s.follow(ctx, run.Name)

	awaitText(t, evs, "step 1")
	for _, msg := range []string{"S1", "S2", "S3"} {
		if code := s.steer(run.Name, msg); code != http.StatusAccepted {
			t.Fatalf("steer %s: status %d, want 202", msg, code)
		}
	}
	awaitText(t, evs, "steer-received: S1")
	awaitText(t, evs, "steer-received: S2\n\nS3")

	if code := s.abort(run.Name); code != http.StatusAccepted {
		t.Fatalf("abort %s: status %d, want 202", run.Name, code)
	}
	s.waitPhase(run.Name, v1alpha1.PhaseFailed, v1alpha1.ReasonCancelled)
	s.waitGone("claim "+run.Name, s.claimKey(run.Name), &extv1beta1.SandboxClaim{})
	if code := s.steer(run.Name, "too late"); code != http.StatusConflict {
		t.Errorf("steer after abort: status %d, want 409", code)
	}
}

func TestForgeTokenRefresh(t *testing.T) {
	s := newStack(t, stackOptions{forge: true})
	gate := filepath.Join(t.TempDir(), "gate")
	run := s.startRun("SCRIPT:steps 1 10ms "+gate, &wire.Workspace{Repo: forgeRepo, BaseRef: "main"})
	ar := s.waitPhase(run.Name, v1alpha1.PhaseRunning, "")
	tokenFile := filepath.Join(s.kubelet.layout(ar.Status.PodName).RunDir, "forge-token.json")

	var minted string
	s.eventually("minted forge token of "+run.Name+" in the runner's token file", func(ctx context.Context) (bool, string) {
		minted = s.secretForgeToken(ctx, run.Name)
		if minted == "" {
			return false, "Secret " + sandbox.SecretName(run.Name) + " has no forge token"
		}
		tok, err := gitcred.ReadToken(tokenFile)
		if err != nil {
			return false, err.Error()
		}
		return tok.Token == minted, "token file holds another token"
	})

	fresh, err := s.forge.Mint(t.Context(), forgeRepo)
	if err != nil {
		t.Fatalf("mint fresh forge token: %v", err)
	}
	s.swapForgeToken(run.Name, fresh)
	s.eventually("swapped-in forge token of "+run.Name+" in the runner's token file", func(context.Context) (bool, string) {
		tok, err := gitcred.ReadToken(tokenFile)
		if err != nil {
			return false, err.Error()
		}
		return tok.Token == fresh.Value && tok.ExpiresAt.Equal(fresh.ExpiresAt.Truncate(time.Second)),
			fmt.Sprintf("token file holds a token expiring %s", tok.ExpiresAt)
	})

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	done := s.waitPhase(run.Name, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if done.Status.FinishedAt == nil {
		t.Fatalf("run %s has no finishedAt", run.Name)
	}
	for _, tok := range []string{minted, fresh.Value} {
		if !slices.ContainsFunc(s.forge.Tokens(), func(ti githubtest.TokenInfo) bool { return ti.Value == tok && ti.Repo == forgeRepo }) {
			t.Errorf("token %s is not among the forge's tokens for %s", tok, forgeRepo)
		}
	}
	finished := done.Status.FinishedAt.Time
	s.eventually("forge tokens of "+run.Name+" revoked", func(context.Context) (bool, string) {
		var live []string
		for _, tok := range s.forge.Tokens() {
			if tok.Repo == forgeRepo && tok.ExpiresAt.After(finished) && !tok.Revoked {
				live = append(live, tok.Value)
			}
		}
		return len(live) == 0, "not revoked: " + strings.Join(live, ", ")
	})
}

func (s *stack) podUIDs() map[string]types.UID {
	s.t.Helper()
	var pods corev1.PodList
	if err := env.Client.List(s.t.Context(), &pods, client.InNamespace(s.cfg.SandboxNamespace)); err != nil {
		s.t.Fatalf("list pods: %v", err)
	}
	out := map[string]types.UID{}
	for _, p := range pods.Items {
		if p.DeletionTimestamp.IsZero() && p.Status.Phase == corev1.PodRunning {
			out[p.Name] = p.UID
		}
	}
	return out
}

func (s *stack) follow(ctx context.Context, run string) <-chan wire.LogEvent {
	s.t.Helper()
	es, err := s.openEvents(ctx, run, 0)
	if err != nil {
		s.t.Fatal(err)
	}
	out := make(chan wire.LogEvent)
	go func() {
		defer close(out)
		defer es.close()
		for {
			ev, err := es.read()
			if err != nil {
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
			if ev.Type == wire.EventEnd {
				return
			}
		}
	}()
	return out
}

func awaitText(t *testing.T, evs <-chan wire.LogEvent, want string) {
	t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-evs:
			if !ok {
				t.Fatalf("event stream ended before text %q", want)
			}
			text, ok := eventText(ev)
			if !ok {
				continue
			}
			if text == want {
				return
			}
			if strings.HasPrefix(text, "steer-received: ") {
				t.Fatalf("got text %q while waiting for %q", text, want)
			}
		case <-timeout:
			t.Fatalf("no text %q within %s", want, waitTimeout)
		}
	}
}

func agentStatus(pod *corev1.Pod) (restarts int32, running bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == sandbox.AgentContainerName {
			return cs.RestartCount, cs.State.Running != nil
		}
	}
	return 0, false
}

func stepNumbers(evs []wire.LogEvent) []int {
	var out []int
	for _, ev := range evs {
		text, ok := eventText(ev)
		if !ok {
			continue
		}
		if m := stepRE.FindStringSubmatch(text); m != nil {
			k, _ := strconv.Atoi(m[1])
			out = append(out, k)
		}
	}
	return out
}

func (s *stack) secretForgeToken(ctx context.Context, run string) string {
	var sec corev1.Secret
	if err := env.Client.Get(ctx, client.ObjectKey{Namespace: s.cfg.RunNamespace, Name: sandbox.SecretName(run)}, &sec); err != nil {
		return ""
	}
	return string(sec.Data[sandbox.SecretKeyForgeToken])
}

func (s *stack) swapForgeToken(run string, tok github.Token) {
	s.t.Helper()
	ctx := s.t.Context()
	entry, err := json.Marshal(map[string]any{"token": tok.Value, "expiresAt": tok.ExpiresAt.UTC()})
	if err != nil {
		s.t.Fatalf("encode live forge token: %v", err)
	}
	key := client.ObjectKey{Namespace: s.cfg.RunNamespace, Name: sandbox.SecretName(run)}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var sec corev1.Secret
		if err := env.Client.Get(ctx, key, &sec); err != nil {
			return err
		}
		var live []json.RawMessage
		if raw := sec.Data[forgeKeyTokensLive]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &live); err != nil {
				return fmt.Errorf("decode %s: %w", forgeKeyTokensLive, err)
			}
		}
		liveJSON, err := json.Marshal(append(live, entry))
		if err != nil {
			return err
		}
		sec.Data[sandbox.SecretKeyForgeToken] = []byte(tok.Value)
		sec.Data[sandbox.SecretKeyForgeExpiresAt] = []byte(tok.ExpiresAt.UTC().Format(time.RFC3339))
		sec.Data[forgeKeyTokensLive] = liveJSON
		return env.Client.Update(ctx, &sec)
	})
	if err != nil {
		s.t.Fatalf("swap forge token into Secret %s: %v", key.Name, err)
	}
}
