package operator_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/sandbox/sandboxtest"
	"github.com/Devcon4/skua/internal/operator/health"
	opprofile "github.com/Devcon4/skua/internal/operator/profile"
	oprun "github.com/Devcon4/skua/internal/operator/run"
)

const runHealthProfile = "health"

type runHealthEnv struct {
	c       client.Client
	cfg     *config.Config
	clock   *clocktesting.FakeClock
	binder  *sandboxtest.Binder
	profile v1alpha1.AgentProfileSpec
}

func startRunHealthEnv(t *testing.T) *runHealthEnv {
	t.Helper()
	e := &runHealthEnv{cfg: isolatedConfig(), clock: clocktesting.NewFakeClock(time.Now())}
	e.c = startOperatorEnv(t, e.cfg, func(mgr ctrl.Manager) error {
		e.binder = sandboxtest.Start(t, mgr, sandboxtest.Options{Clock: e.clock})
		return health.Setup(mgr, health.Deps{Config: e.cfg, APIReader: mgr.GetAPIReader(), Clock: e.clock})
	})

	ctx := t.Context()
	ensureRuntimeClass(t, e.c, e.cfg.Defaults.RuntimeClassName)
	profile := &v1alpha1.AgentProfile{
		ObjectMeta: testProfileMeta(e.cfg, runHealthProfile),
		Spec: v1alpha1.AgentProfileSpec{
			Image:   "generic:dev",
			Command: []string{"omp"},
			Models:  []string{"fake-model"},
		},
	}
	if err := e.c.Create(ctx, profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	e.profile = profile.Spec
	if err := e.c.Create(ctx, opprofile.Template(profile, e.cfg)); err != nil {
		t.Fatalf("create sandbox template: %v", err)
	}
	if err := e.c.Create(ctx, opprofile.WarmPool(profile, e.cfg)); err != nil {
		t.Fatalf("create warm pool: %v", err)
	}
	return e
}

func (e *runHealthEnv) startRun(t *testing.T, name string, maxRuntime time.Duration, mutateClaim func(*extv1beta1.SandboxClaim)) (run, pod client.ObjectKey) {
	t.Helper()
	r, p := e.bindRun(t, name, mutateClaim)
	e.markRunning(t, r, p, maxRuntime)
	return client.ObjectKeyFromObject(r), client.ObjectKeyFromObject(p)
}

func (e *runHealthEnv) bindRun(t *testing.T, name string, mutateClaim func(*extv1beta1.SandboxClaim)) (*v1alpha1.AgentRun, *corev1.Pod) {
	t.Helper()
	ctx := t.Context()
	r := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.cfg.RunNamespace, Name: name},
		Spec: v1alpha1.AgentRunSpec{
			Profile:   runHealthProfile,
			ClientKey: name,
			Task:      v1alpha1.Task{Prompt: "work"},
		},
	}
	if err := e.c.Create(ctx, r); err != nil {
		t.Fatalf("create run %s: %v", name, err)
	}
	claim := oprun.Claim(r, e.cfg)
	if mutateClaim != nil {
		mutateClaim(claim)
	}
	if err := e.c.Create(ctx, claim); err != nil {
		t.Fatalf("create claim %s: %v", name, err)
	}
	runHealthEventually(t, "claim "+name+" bound", func() bool {
		if err := e.c.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
			t.Fatalf("get claim %s: %v", name, err)
		}
		return claim.Status.SandboxStatus.Name != ""
	})
	var p corev1.Pod
	if err := e.c.Get(ctx, client.ObjectKey{Namespace: e.cfg.SandboxNamespace, Name: claim.Status.SandboxStatus.Name}, &p); err != nil {
		t.Fatalf("get pod of claim %s: %v", name, err)
	}
	return r, &p
}

func (e *runHealthEnv) markRunning(t *testing.T, r *v1alpha1.AgentRun, p *corev1.Pod, maxRuntime time.Duration) {
	t.Helper()
	snapshot := e.profile.DeepCopy()
	snapshot.Limits.MaxRuntime = metav1.Duration{Duration: maxRuntime}
	snapshot.Limits.MaxRestarts = ptr.To[int32](3)
	r.Status = v1alpha1.AgentRunStatus{
		Phase:           v1alpha1.PhaseRunning,
		Claim:           r.Name,
		PodName:         p.Name,
		PodUID:          p.UID,
		StartedAt:       ptr.To(metav1.NewTime(e.clock.Now())),
		ProfileSnapshot: snapshot,
	}
	if err := e.c.Status().Update(t.Context(), r); err != nil {
		t.Fatalf("mark run %s running: %v", r.Name, err)
	}
}

func (e *runHealthEnv) run(t *testing.T, key client.ObjectKey) *v1alpha1.AgentRun {
	t.Helper()
	var r v1alpha1.AgentRun
	if err := e.c.Get(t.Context(), key, &r); err != nil {
		t.Fatalf("get run %s: %v", key.Name, err)
	}
	return &r
}

func (e *runHealthEnv) poke(t *testing.T, key client.ObjectKey) {
	t.Helper()
	r := e.run(t, key)
	base := r.DeepCopy()
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	r.Annotations["test.skua.dev/poke"] = strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := e.c.Patch(t.Context(), r, client.MergeFrom(base)); err != nil {
		t.Fatalf("poke run %s: %v", key.Name, err)
	}
}

func (e *runHealthEnv) waitFailed(t *testing.T, key client.ObjectKey, reason string) *v1alpha1.AgentRun {
	t.Helper()
	var r *v1alpha1.AgentRun
	runHealthEventually(t, "run "+key.Name+" Failed", func() bool {
		r = e.run(t, key)
		return r.Status.Phase.Terminal()
	})
	if r.Status.Phase != v1alpha1.PhaseFailed || r.Status.Reason != reason {
		t.Fatalf("run %s ended %s/%s (%s), want Failed/%s", key.Name, r.Status.Phase, r.Status.Reason, r.Status.Message, reason)
	}
	if r.Status.FinishedAt == nil {
		t.Errorf("run %s has no finishedAt", key.Name)
	}
	return r
}

func (e *runHealthEnv) assertRunning(t *testing.T, key client.ObjectKey) {
	t.Helper()
	if r := e.run(t, key); r.Status.Phase != v1alpha1.PhaseRunning || r.Status.Reason != "" {
		t.Errorf("run %s is %s/%s (%s), want Running", key.Name, r.Status.Phase, r.Status.Reason, r.Status.Message)
	}
}

func runHealthEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunHealth(t *testing.T) {
	t.Parallel()
	e := startRunHealthEnv(t)

	t.Run("CrashLoop", func(t *testing.T) {
		atLimit, atLimitPod := e.startRun(t, "health-restarts-3", 4*time.Hour, nil)
		overLimit, overLimitPod := e.startRun(t, "health-restarts-4", 4*time.Hour, nil)

		e.binder.RestartContainer(atLimitPod, 3)
		e.binder.RestartContainer(overLimitPod, 4)

		e.waitFailed(t, overLimit, v1alpha1.ReasonCrashLoop)
		e.assertRunning(t, atLimit)
	})

	t.Run("PodDeleted", func(t *testing.T) {
		stale, stalePod := e.startRun(t, "health-stale-claim", 4*time.Hour, func(c *extv1beta1.SandboxClaim) {
			c.Labels[sandbox.LabelRunUID] = "uid-of-an-earlier-run"
		})
		replacedRun, replacedPod := e.bindRun(t, "health-replaced", nil)
		lost, lostPod := e.startRun(t, "health-lost", 4*time.Hour, nil)

		e.binder.RestartContainer(stalePod, 10)
		e.binder.Evict(stalePod, "stale pod evicted")
		e.poke(t, stale)
		e.binder.DeletePod(client.ObjectKeyFromObject(replacedPod))
		e.markRunning(t, replacedRun, replacedPod, 4*time.Hour)
		e.binder.LosePod(lostPod)

		if got := e.waitFailed(t, client.ObjectKeyFromObject(replacedRun), v1alpha1.ReasonPodLost); !strings.Contains(got.Status.Message, "was replaced") {
			t.Errorf("replaced pod message = %q, want it to contain %q", got.Status.Message, "was replaced")
		}
		if got := e.waitFailed(t, lost, v1alpha1.ReasonPodLost); !strings.Contains(got.Status.Message, "is gone") {
			t.Errorf("lost pod message = %q, want it to contain %q", got.Status.Message, "is gone")
		}
		e.assertRunning(t, stale)
	})

	t.Run("Evicted", func(t *testing.T) {
		run, pod := e.startRun(t, "health-evicted", 4*time.Hour, nil)
		msg := "The node was low on resource: memory."

		e.binder.Evict(pod, msg)

		got := e.waitFailed(t, run, v1alpha1.ReasonPodLost)
		if !strings.Contains(got.Status.Message, msg) {
			t.Errorf("message = %q, want it to contain %q", got.Status.Message, msg)
		}
	})

	t.Run("MaxRuntime", func(t *testing.T) {
		past, _ := e.startRun(t, "health-past-deadline", time.Hour, nil)
		before, _ := e.startRun(t, "health-before-deadline", 3*time.Hour, nil)
		done, donePod := e.startRun(t, "health-terminal", time.Hour, nil)

		finished := e.run(t, done)
		finished.Status.Phase = v1alpha1.PhaseSucceeded
		finished.Status.Reason = v1alpha1.ReasonCompleted
		finished.Status.Message = "done"
		finished.Status.FinishedAt = ptr.To(metav1.NewTime(e.clock.Now().Truncate(time.Second)))
		if err := e.c.Status().Update(t.Context(), finished); err != nil {
			t.Fatalf("finish run: %v", err)
		}

		e.clock.Step(2 * time.Hour)
		e.binder.DeletePod(donePod)
		e.poke(t, done)
		e.poke(t, before)
		e.poke(t, past)

		e.waitFailed(t, past, v1alpha1.ReasonMaxRuntime)
		e.assertRunning(t, before)
		got := e.run(t, done)
		if got.Status.Phase != v1alpha1.PhaseSucceeded || got.Status.Reason != v1alpha1.ReasonCompleted ||
			got.Status.Message != "done" || !got.Status.FinishedAt.Equal(finished.Status.FinishedAt) {
			t.Errorf("terminal run rewritten to %s/%s (%s) finishedAt %v", got.Status.Phase, got.Status.Reason, got.Status.Message, got.Status.FinishedAt)
		}
	})
}
