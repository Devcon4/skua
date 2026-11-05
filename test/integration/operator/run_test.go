package operator_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	clocktesting "k8s.io/utils/clock/testing"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/sandbox/sandboxtest"
	"github.com/Devcon4/skua/internal/integration/litellm"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
	opprofile "github.com/Devcon4/skua/internal/operator/profile"
	oprun "github.com/Devcon4/skua/internal/operator/run"
)

const (
	runTestColdDelay = 10 * time.Second
	runTestOwner     = "0123456789abcdef0123456789abcdef01234567"
	runTestModel     = "fake-model"
	runTestWait      = 20 * time.Second
)

type runEnv struct {
	c      client.Client
	cfg    *config.Config
	clock  *clocktesting.FakeClock
	gw     *litellmtest.Fake
	binder *sandboxtest.Binder
	pokes  atomic.Int64
}

func runStartEnv(t *testing.T, wrap func(*litellmtest.Fake) litellm.Provider, timing oprun.Timing) *runEnv {
	t.Helper()
	e := &runEnv{
		cfg:   isolatedConfig(),
		clock: clocktesting.NewFakeClock(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)),
	}
	e.gw = litellmtest.New(e.clock)
	e.gw.AddModel(runTestModel, true)
	var gateway litellm.Provider = e.gw
	if wrap != nil {
		gateway = wrap(e.gw)
	}
	if timing.PodPoll == 0 {
		timing.PodPoll = 20 * time.Millisecond
	}
	if timing.RetryMax == 0 {
		timing.RetryMax = 500 * time.Millisecond
	}
	e.c = startOperatorEnv(t, e.cfg, func(mgr ctrl.Manager) error {
		e.binder = sandboxtest.Start(t, mgr, sandboxtest.Options{ColdDelay: runTestColdDelay, Clock: e.clock})
		return oprun.Setup(mgr, oprun.Deps{Config: e.cfg, Gateway: gateway, APIReader: mgr.GetAPIReader(), Clock: e.clock, Timing: timing})
	})
	ensureRuntimeClass(t, e.c, e.cfg.Defaults.RuntimeClassName)
	return e
}

func (e *runEnv) createProfile(t *testing.T, name string) client.ObjectKey {
	t.Helper()
	ctx := t.Context()
	p := &v1alpha1.AgentProfile{
		ObjectMeta: testProfileMeta(e.cfg, name),
		Spec: v1alpha1.AgentProfileSpec{
			Image:   "generic:dev",
			Command: []string{"omp"},
			Models:  []string{runTestModel},
		},
	}
	if err := e.c.Create(ctx, p); err != nil {
		t.Fatalf("create profile %s: %v", name, err)
	}
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonReady, ObservedGeneration: p.Generation,
	})
	if err := e.c.Status().Update(ctx, p); err != nil {
		t.Fatalf("mark profile %s Ready: %v", name, err)
	}
	if err := e.c.Create(ctx, opprofile.Template(p, e.cfg)); err != nil {
		t.Fatalf("create template of %s: %v", name, err)
	}
	pool := opprofile.WarmPool(p, e.cfg)
	if err := e.c.Create(ctx, pool); err != nil {
		t.Fatalf("create pool of %s: %v", name, err)
	}
	return client.ObjectKeyFromObject(pool)
}

func (e *runEnv) warmPod(t *testing.T, pool client.ObjectKey, profile string) corev1.Pod {
	t.Helper()
	e.binder.SetPoolReady(pool, 1)
	var pods corev1.PodList
	if err := e.c.List(t.Context(), &pods, client.InNamespace(e.cfg.SandboxNamespace),
		client.MatchingLabels{sandbox.LabelProfile: profile}); err != nil {
		t.Fatalf("list pool pods: %v", err)
	}
	for _, p := range pods.Items {
		if p.Labels[extv1beta1.SandboxIDLabel] == "" && p.DeletionTimestamp.IsZero() {
			return p
		}
	}
	t.Fatalf("pool %s has no unclaimed pod", pool)
	return corev1.Pod{}
}

func (e *runEnv) createRun(t *testing.T, name, profile string) *v1alpha1.AgentRun {
	t.Helper()
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: e.cfg.RunNamespace,
			Labels:    map[string]string{v1alpha1.LabelOwner: runTestOwner},
		},
		Spec: v1alpha1.AgentRunSpec{Profile: profile, ClientKey: name, Task: v1alpha1.Task{Prompt: "fix the bug"}},
	}
	if err := e.c.Create(t.Context(), run); err != nil {
		t.Fatalf("create run %s: %v", name, err)
	}
	return run
}

func (e *runEnv) runningRun(t *testing.T, name string, pool client.ObjectKey, profile string) (*v1alpha1.AgentRun, corev1.Pod) {
	t.Helper()
	pod := e.warmPod(t, pool, profile)
	e.createRun(t, name, profile)
	run := e.waitRun(t, name, "Running", func(r *v1alpha1.AgentRun) bool { return r.Status.Phase == v1alpha1.PhaseRunning })
	return run, pod
}

func (e *runEnv) getRun(t *testing.T, name string) (*v1alpha1.AgentRun, bool) {
	t.Helper()
	var run v1alpha1.AgentRun
	err := e.c.Get(t.Context(), client.ObjectKey{Namespace: e.cfg.RunNamespace, Name: name}, &run)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get run %s: %v", name, err)
	}
	return &run, true
}

func (e *runEnv) waitRun(t *testing.T, name, what string, cond func(*v1alpha1.AgentRun) bool) *v1alpha1.AgentRun {
	t.Helper()
	var run *v1alpha1.AgentRun
	runEventually(t, runTestWait, "run "+name+" "+what, func() bool {
		var ok bool
		run, ok = e.getRun(t, name)
		return ok && cond(run)
	})
	return run
}

func (e *runEnv) waitRunGone(t *testing.T, name string) {
	t.Helper()
	runEventually(t, runTestWait, "run "+name+" deleted", func() bool {
		_, ok := e.getRun(t, name)
		return !ok
	})
}

func (e *runEnv) claim(t *testing.T, name string) (*extv1beta1.SandboxClaim, bool) {
	t.Helper()
	var claim extv1beta1.SandboxClaim
	err := e.c.Get(t.Context(), client.ObjectKey{Namespace: e.cfg.SandboxNamespace, Name: name}, &claim)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get claim %s: %v", name, err)
	}
	return &claim, true
}

func (e *runEnv) waitClaimGone(t *testing.T, name string) {
	t.Helper()
	runEventually(t, runTestWait, "claim "+name+" deleted", func() bool {
		_, ok := e.claim(t, name)
		return !ok
	})
}

func (e *runEnv) poke(t *testing.T, name string) {
	t.Helper()
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"test.skua.dev/poke":%q}}}`, strconv.FormatInt(e.pokes.Add(1), 10))
	run := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: e.cfg.RunNamespace, Name: name}}
	if err := e.c.Patch(t.Context(), run, client.RawPatch(types.MergePatchType, []byte(patch))); err != nil {
		t.Fatalf("poke run %s: %v", name, err)
	}
}

func (e *runEnv) markFailed(t *testing.T, name, reason string) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run, ok := e.getRun(t, name)
		if !ok {
			return fmt.Errorf("run %s is gone", name)
		}
		run.Status.Phase = v1alpha1.PhaseFailed
		run.Status.Reason = reason
		return e.c.Status().Update(t.Context(), run)
	})
	if err != nil {
		t.Fatalf("mark run %s Failed: %v", name, err)
	}
}

func (e *runEnv) calls(op litellmtest.Op, alias string) []litellmtest.Call {
	var out []litellmtest.Call
	for _, c := range e.gw.Calls() {
		if c.Op == op && c.Target == alias {
			out = append(out, c)
		}
	}
	return out
}

func (e *runEnv) succeeded(op litellmtest.Op, alias string) int {
	n := 0
	for _, c := range e.calls(op, alias) {
		if c.Err == nil {
			n++
		}
	}
	return n
}

func runEventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runConsistently(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s did not hold", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func runIsFailed(reason string) func(*v1alpha1.AgentRun) bool {
	return func(r *v1alpha1.AgentRun) bool {
		return r.Status.Phase == v1alpha1.PhaseFailed && r.Status.Reason == reason
	}
}

func runConditionIs(condType string, status metav1.ConditionStatus) func(*v1alpha1.AgentRun) bool {
	return func(r *v1alpha1.AgentRun) bool {
		c := meta.FindStatusCondition(r.Status.Conditions, condType)
		return c != nil && c.Status == status
	}
}

func runParallelEnv(t *testing.T, profile string) (*runEnv, client.ObjectKey) {
	t.Helper()
	t.Parallel()
	e := runStartEnv(t, nil, oprun.Timing{})
	return e, e.createProfile(t, profile)
}

func (e *runEnv) profilelessRun(t *testing.T, name string) *v1alpha1.AgentRun {
	t.Helper()
	e.createRun(t, name, "absent")
	return e.waitRun(t, name, "Failed/ProfileNotFound", runIsFailed(v1alpha1.ReasonProfileNotFound))
}

func TestRunReconciler(t *testing.T) {
	t.Parallel()

	t.Run("warm adoption mints one key and records the adopted pod", func(t *testing.T) {
		e, warmPool := runParallelEnv(t, "warm")
		run, pod := e.runningRun(t, "warm-adopt", warmPool, "warm")
		alias := "skua-" + string(run.UID)

		if !slices.Contains(run.Finalizers, oprun.Finalizer) {
			t.Errorf("finalizers = %v, want %s", run.Finalizers, oprun.Finalizer)
		}
		if run.Status.PodName != pod.Name || run.Status.PodUID != pod.UID || run.Status.Claim != run.Name || run.Status.StartedAt == nil {
			t.Errorf("status claim %q pod %q uid %q startedAt %v, want claim %q pod %q uid %q", run.Status.Claim, run.Status.PodName, run.Status.PodUID, run.Status.StartedAt, run.Name, pod.Name, pod.UID)
		}
		if run.Status.ProfileSnapshot == nil || run.Status.ProfileSnapshot.Image != "generic:dev" {
			t.Errorf("profileSnapshot = %+v, want the profile spec", run.Status.ProfileSnapshot)
		}

		var secret corev1.Secret
		if err := e.c.Get(t.Context(), client.ObjectKey{Namespace: e.cfg.RunNamespace, Name: sandbox.SecretName("warm-adopt")}, &secret); err != nil {
			t.Fatalf("get run secret: %v", err)
		}
		if !metav1.IsControlledBy(&secret, run) {
			t.Errorf("secret owner references = %+v, want controlled by run %s", secret.OwnerReferences, run.UID)
		}
		if secret.Labels[sandbox.LabelRunSecret] != "true" || secret.Labels[sandbox.LabelRunName] != run.Name {
			t.Errorf("secret labels = %v", secret.Labels)
		}
		value := string(secret.Data[sandbox.SecretKeyLiteLLM])
		if !strings.HasPrefix(value, oprun.KeyValuePrefix) || len(value) != len(oprun.KeyValuePrefix)+32 {
			t.Errorf("litellmKey has length %d or wrong prefix", len(value))
		}

		key, ok := e.gw.Keys()[alias]
		switch {
		case !ok:
			t.Fatalf("no key with alias %s; keys %v", alias, e.gw.Keys())
		case key.Value != value:
			t.Error("minted key value differs from the Secret's litellmKey")
		case key.MaxBudget != 10 || key.Duration != 4*time.Hour+5*time.Minute:
			t.Errorf("key budget %v duration %s, want 10 and 4h5m", key.MaxBudget, key.Duration)
		case len(key.Models) != 1 || key.Models[0] != runTestModel:
			t.Errorf("key models = %v", key.Models)
		}
		wantMeta := map[string]string{"run": run.Name, "namespace": run.Namespace, "profile": "warm", "owner": runTestOwner}
		if !apiequality.Semantic.DeepEqual(key.Metadata, wantMeta) {
			t.Errorf("key metadata = %v, want %v", key.Metadata, wantMeta)
		}
		if n := len(e.calls(litellmtest.OpMint, alias)); n != 1 {
			t.Errorf("mint calls = %d, want 1", n)
		}

		claim, ok := e.claim(t, run.Name)
		if !ok {
			t.Fatal("claim is missing")
		}
		if len(claim.OwnerReferences) != 0 {
			t.Errorf("claim owner references = %+v, want none", claim.OwnerReferences)
		}
		wantSpec := extv1beta1.SandboxClaimSpec{WarmPoolRef: extv1beta1.SandboxWarmPoolRef{Name: warmPool.Name}}
		if !apiequality.Semantic.DeepEqual(claim.Spec, wantSpec) {
			t.Errorf("claim spec = %+v, want only warmPoolRef %s", claim.Spec, warmPool.Name)
		}
		if claim.Labels[sandbox.LabelRunName] != run.Name || claim.Labels[sandbox.LabelRunUID] != string(run.UID) || claim.Labels[sandbox.LabelProfile] != "warm" {
			t.Errorf("claim labels = %v", claim.Labels)
		}
		if claim.Annotations[extv1beta1.AssignedSandboxNameAnnotation] != pod.Name {
			t.Errorf("warm claim sandbox-name annotation = %q, want %q", claim.Annotations[extv1beta1.AssignedSandboxNameAnnotation], pod.Name)
		}

		e.poke(t, run.Name)
		runConsistently(t, "no second mint", func() bool { return len(e.calls(litellmtest.OpMint, alias)) == 1 })
	})

	t.Run("cold claim turns Running after ColdDelay", func(t *testing.T) {
		e, _ := runParallelEnv(t, "cold")
		run := e.createRun(t, "cold-start", "cold")
		runEventually(t, runTestWait, "cold claim created", func() bool {
			_, ok := e.claim(t, run.Name)
			return ok
		})
		runEventually(t, runTestWait, "binder holding the cold claim", func() bool {
			return e.binder.Waiting(client.ObjectKey{Namespace: e.cfg.SandboxNamespace, Name: run.Name})
		})
		claim, _ := e.claim(t, run.Name)
		if claim.Status.SandboxStatus.Name != "" {
			t.Fatalf("cold claim bound before ColdDelay to %q", claim.Status.SandboxStatus.Name)
		}
		if got, _ := e.getRun(t, run.Name); got.Status.Phase != v1alpha1.PhasePending {
			t.Fatalf("phase before ColdDelay = %q, want Pending", got.Status.Phase)
		}

		e.clock.Step(runTestColdDelay)
		got := e.waitRun(t, run.Name, "Running", func(r *v1alpha1.AgentRun) bool { return r.Status.Phase == v1alpha1.PhaseRunning })
		if got.Status.PodName != run.Name {
			t.Errorf("podName = %q, want the cold pod %q", got.Status.PodName, run.Name)
		}
		claim, _ = e.claim(t, run.Name)
		if v, ok := claim.Annotations[extv1beta1.AssignedSandboxNameAnnotation]; ok {
			t.Errorf("cold claim has sandbox-name annotation %q", v)
		}
	})

	t.Run("mint failing twice then succeeding mints once", func(t *testing.T) {
		e, pool := runParallelEnv(t, "mint-retry")
		e.gw.FailNext(litellmtest.OpMint, &litellm.APIError{Status: http.StatusInternalServerError})
		e.gw.FailNext(litellmtest.OpMint, &litellm.APIError{Status: http.StatusInternalServerError})
		run, _ := e.runningRun(t, "mint-retry", pool, "mint-retry")
		alias := "skua-" + string(run.UID)
		calls := e.calls(litellmtest.OpMint, alias)
		if len(calls) != 3 || calls[0].Err == nil || calls[1].Err == nil || calls[2].Err != nil {
			t.Fatalf("mint calls = %+v, want two failures then one success", calls)
		}
		if _, ok := e.gw.Keys()[alias]; !ok {
			t.Fatalf("no key with alias %s", alias)
		}
	})

	t.Run("alias conflict fails the run with GatewayError", func(t *testing.T) {
		e, _ := runParallelEnv(t, "alias-conflict")
		e.gw.FailNext(litellmtest.OpMint, fmt.Errorf("%w: test", litellm.ErrAliasConflict))
		e.createRun(t, "alias-conflict", "alias-conflict")
		got := e.waitRun(t, "alias-conflict", "Failed/GatewayError", runIsFailed(v1alpha1.ReasonGatewayError))
		if got.Status.FinishedAt == nil {
			t.Error("finishedAt is unset")
		}
		if _, ok := e.claim(t, "alias-conflict"); ok {
			t.Error("a claim exists for a run whose mint conflicted")
		}
	})

	t.Run("mint failing for 5 minutes fails the run with GatewayError", func(t *testing.T) {
		e, _ := runParallelEnv(t, "mint-down")
		e.gw.Unavailable(true)
		e.createRun(t, "mint-down", "mint-down")
		e.waitRun(t, "mint-down", "KeyMinted=False", runConditionIs(oprun.ConditionKeyMinted, metav1.ConditionFalse))
		e.clock.Step(4 * time.Minute)
		e.poke(t, "mint-down")
		runConsistently(t, "run stays Pending before 5 minutes", func() bool {
			r, ok := e.getRun(t, "mint-down")
			return ok && r.Status.Phase == v1alpha1.PhasePending
		})
		e.clock.Step(time.Minute)
		e.poke(t, "mint-down")
		e.waitRun(t, "mint-down", "Failed/GatewayError", runIsFailed(v1alpha1.ReasonGatewayError))
		e.gw.Unavailable(false)
		e.poke(t, "mint-down")
		e.waitRun(t, "mint-down", "KeyRevoked=True", runConditionIs(oprun.ConditionKeyRevoked, metav1.ConditionTrue))
		if _, ok := e.claim(t, "mint-down"); ok {
			t.Error("a claim exists for a run that never minted")
		}
	})

	t.Run("missing profile fails the run with no claim and no mint", func(t *testing.T) {
		t.Parallel()
		e := runStartEnv(t, nil, oprun.Timing{})
		got := e.profilelessRun(t, "no-profile")
		if got.Status.ProfileSnapshot != nil {
			t.Error("profileSnapshot set for a missing profile")
		}
		if n := len(e.calls(litellmtest.OpMint, "skua-"+string(got.UID))); n != 0 {
			t.Errorf("mint calls = %d, want 0", n)
		}
		e.poke(t, got.Name)
		runConsistently(t, "no claim", func() bool {
			_, ok := e.claim(t, got.Name)
			return !ok
		})
	})

	t.Run("deleting the profile after the snapshot leaves the run untouched", func(t *testing.T) {
		e, pool := runParallelEnv(t, "doomed")
		run, _ := e.runningRun(t, "doomed-run", pool, "doomed")
		if err := e.c.Delete(t.Context(), &v1alpha1.AgentProfile{ObjectMeta: metav1.ObjectMeta{Name: "doomed"}}); err != nil {
			t.Fatalf("delete profile: %v", err)
		}
		e.poke(t, run.Name)
		runConsistently(t, "run stays Running", func() bool {
			r, ok := e.getRun(t, run.Name)
			return ok && r.Status.Phase == v1alpha1.PhaseRunning && r.Status.Reason == ""
		})
	})

	t.Run("a Ready condition from an older profile generation does not admit", func(t *testing.T) {
		e, _ := runParallelEnv(t, "edited")
		var p v1alpha1.AgentProfile
		if err := e.c.Get(t.Context(), client.ObjectKey{Name: "edited"}, &p); err != nil {
			t.Fatalf("get profile: %v", err)
		}
		p.Spec.Image = "generic:v2"
		if err := e.c.Update(t.Context(), &p); err != nil {
			t.Fatalf("edit profile: %v", err)
		}
		run := e.createRun(t, "stale-ready", "edited")
		runConsistently(t, "run not admitted", func() bool {
			r, ok := e.getRun(t, run.Name)
			return ok && r.Status.ProfileSnapshot == nil && r.Status.Phase == ""
		})

		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonReady, ObservedGeneration: p.Generation,
		})
		if err := e.c.Status().Update(t.Context(), &p); err != nil {
			t.Fatalf("mark profile Ready for generation %d: %v", p.Generation, err)
		}
		e.poke(t, run.Name)
		got := e.waitRun(t, run.Name, "admitted", func(r *v1alpha1.AgentRun) bool { return r.Status.ProfileSnapshot != nil })
		if got.Status.ProfileSnapshot.Image != "generic:v2" {
			t.Errorf("snapshot image = %q, want generic:v2", got.Status.ProfileSnapshot.Image)
		}
	})

	t.Run("a Failed run revokes its key once and deletes its claim", func(t *testing.T) {
		e, pool := runParallelEnv(t, "cancelled")
		run, _ := e.runningRun(t, "cancelled", pool, "cancelled")
		alias := "skua-" + string(run.UID)
		e.markFailed(t, run.Name, v1alpha1.ReasonCancelled)
		e.waitRun(t, run.Name, "KeyRevoked=True with finishedAt", func(r *v1alpha1.AgentRun) bool {
			return runConditionIs(oprun.ConditionKeyRevoked, metav1.ConditionTrue)(r) && r.Status.FinishedAt != nil
		})
		e.waitClaimGone(t, run.Name)
		if !e.gw.Keys()[alias].Revoked {
			t.Errorf("key %s is not revoked", alias)
		}
		e.poke(t, run.Name)
		runConsistently(t, "one revoke", func() bool { return len(e.calls(litellmtest.OpRevoke, alias)) == 1 })
		if got, _ := e.getRun(t, run.Name); got.Status.Reason != v1alpha1.ReasonCancelled {
			t.Errorf("reason = %q, want %s", got.Status.Reason, v1alpha1.ReasonCancelled)
		}
	})

	t.Run("a terminal run whose revoke fails still deletes its claim and sets finishedAt", func(t *testing.T) {
		e, pool := runParallelEnv(t, "revoke-fails")
		run, _ := e.runningRun(t, "revoke-fails-terminal", pool, "revoke-fails")
		e.gw.Unavailable(true)
		t.Cleanup(func() { e.gw.Unavailable(false) })
		e.markFailed(t, run.Name, v1alpha1.ReasonCancelled)
		e.waitRun(t, run.Name, "KeyRevoked=False with finishedAt", func(r *v1alpha1.AgentRun) bool {
			return runConditionIs(oprun.ConditionKeyRevoked, metav1.ConditionFalse)(r) && r.Status.FinishedAt != nil
		})
		e.waitClaimGone(t, run.Name)
		e.gw.Unavailable(false)
		e.poke(t, run.Name)
		e.waitRun(t, run.Name, "KeyRevoked=True", runConditionIs(oprun.ConditionKeyRevoked, metav1.ConditionTrue))
	})

	t.Run("deleting a run revokes, deletes the claim and releases the finalizer", func(t *testing.T) {
		e, pool := runParallelEnv(t, "deleted")
		run, _ := e.runningRun(t, "deleted", pool, "deleted")
		alias := "skua-" + string(run.UID)
		if err := e.c.Delete(t.Context(), run); err != nil {
			t.Fatalf("delete run: %v", err)
		}
		e.waitRunGone(t, run.Name)
		e.waitClaimGone(t, run.Name)
		if e.succeeded(litellmtest.OpRevoke, alias) != 1 || !e.gw.Keys()[alias].Revoked {
			t.Errorf("revoke calls = %+v, want one success", e.calls(litellmtest.OpRevoke, alias))
		}
	})

	t.Run("a revoke failing for 2 minutes still releases the finalizer", func(t *testing.T) {
		e, pool := runParallelEnv(t, "revoke-down")
		run, _ := e.runningRun(t, "revoke-down", pool, "revoke-down")
		e.gw.Unavailable(true)
		t.Cleanup(func() { e.gw.Unavailable(false) })
		if err := e.c.Delete(t.Context(), run); err != nil {
			t.Fatalf("delete run: %v", err)
		}
		e.waitRun(t, run.Name, "KeyRevoked=False", runConditionIs(oprun.ConditionKeyRevoked, metav1.ConditionFalse))
		e.clock.Step(time.Minute)
		e.poke(t, run.Name)
		runConsistently(t, "finalizer held before 2 minutes", func() bool {
			_, ok := e.getRun(t, run.Name)
			return ok
		})
		e.clock.Step(time.Minute)
		e.poke(t, run.Name)
		e.waitRunGone(t, run.Name)
		e.waitClaimGone(t, run.Name)
	})

	t.Run("a run fetched longer than afterFetch ago is deleted", func(t *testing.T) {
		t.Parallel()
		e := runStartEnv(t, nil, oprun.Timing{})
		run := e.profilelessRun(t, "fetched")
		fetched := e.clock.Now().Add(-e.cfg.Retention.AfterFetch.Duration + time.Hour).Format(time.RFC3339)
		e.annotateFetched(t, run.Name, fetched)
		runConsistently(t, "run kept inside afterFetch", func() bool {
			_, ok := e.getRun(t, run.Name)
			return ok
		})
		fetched = e.clock.Now().Add(-e.cfg.Retention.AfterFetch.Duration - time.Minute).Format(time.RFC3339)
		e.annotateFetched(t, run.Name, fetched)
		e.waitRunGone(t, run.Name)
	})

	t.Run("a terminal run without fetched-at is deleted after retention.max", func(t *testing.T) {
		t.Parallel()
		e := runStartEnv(t, nil, oprun.Timing{})
		got := e.profilelessRun(t, "no-profile")
		e.clock.SetTime(got.Status.FinishedAt.Add(e.cfg.Retention.Max.Duration - time.Minute))
		e.poke(t, "no-profile")
		runConsistently(t, "run kept inside retention.max", func() bool {
			_, ok := e.getRun(t, "no-profile")
			return ok
		})
		e.clock.Step(time.Minute)
		e.poke(t, "no-profile")
		e.waitRunGone(t, "no-profile")
	})
}

func (e *runEnv) annotateFetched(t *testing.T, name, fetchedAt string) {
	t.Helper()
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, v1alpha1.AnnotationFetchedAt, fetchedAt)
	run := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: e.cfg.RunNamespace, Name: name}}
	if err := e.c.Patch(t.Context(), run, client.RawPatch(types.MergePatchType, []byte(patch))); err != nil {
		t.Fatalf("annotate run %s: %v", name, err)
	}
}

type runSlowFirstRevoke struct {
	*litellmtest.Fake
	once sync.Once
}

func (g *runSlowFirstRevoke) Revoke(ctx context.Context, alias string) error {
	err := g.Fake.Revoke(ctx, alias)
	g.once.Do(func() { g.Delay(litellmtest.OpRevoke, 0) })
	return err
}

func TestRunRevokeAttemptTimeout(t *testing.T) {
	t.Parallel()
	const attempt = 500 * time.Millisecond
	e := runStartEnv(t, func(f *litellmtest.Fake) litellm.Provider { return &runSlowFirstRevoke{Fake: f} }, oprun.Timing{RevokeAttempt: attempt})
	pool := e.createProfile(t, "slow")
	run, _ := e.runningRun(t, "slow-revoke", pool, "slow")
	alias := "skua-" + string(run.UID)

	e.gw.Delay(litellmtest.OpRevoke, runTestWait)
	start := time.Now()
	e.markFailed(t, run.Name, v1alpha1.ReasonCancelled)
	runEventually(t, runTestWait, "run slow-revoke KeyRevoked=True", func() bool {
		r, ok := e.getRun(t, run.Name)
		return ok && runConditionIs(oprun.ConditionKeyRevoked, metav1.ConditionTrue)(r)
	})
	elapsed := time.Since(start)

	calls := e.calls(litellmtest.OpRevoke, alias)
	if len(calls) < 2 || !errors.Is(calls[0].Err, context.DeadlineExceeded) || calls[len(calls)-1].Err != nil {
		t.Fatalf("revoke calls = %+v, want a timed-out attempt then a success", calls)
	}
	if elapsed < attempt || elapsed > attempt+5*time.Second {
		t.Errorf("revoke settled after %s, want just over %s", elapsed, attempt)
	}
	e.waitClaimGone(t, run.Name)
}
