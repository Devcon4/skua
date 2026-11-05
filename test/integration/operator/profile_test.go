package operator_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/sandbox/sandboxtest"
	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
	"github.com/Devcon4/skua/internal/operator/compat"
	opprofile "github.com/Devcon4/skua/internal/operator/profile"
)

const profileWaitTimeout = 30 * time.Second

type profileEnv struct {
	c      client.Client
	cfg    *config.Config
	gw     *litellmtest.Fake
	clock  *testingclock.FakeClock
	binder *sandboxtest.Binder
	compat *compat.Check
}

func TestProfileReconciler(t *testing.T) {
	t.Parallel()
	cfg := isolatedConfig()
	clk := testingclock.NewFakeClock(time.Now())
	gw := litellmtest.New(clk)
	gw.AddModel("priced", true, "fakellm")
	gw.AddModel("unpriced", false, "fakellm")
	env := &profileEnv{cfg: cfg, gw: gw, clock: clk, compat: compat.NewCheck(nil, "", time.Hour)}
	env.c = startOperatorEnv(t, cfg, func(mgr ctrl.Manager) error {
		env.binder = sandboxtest.Start(t, mgr, sandboxtest.Options{Clock: clk})
		return opprofile.Setup(mgr, opprofile.Deps{Config: cfg, Gateway: gw, Clock: clk, Compat: env.compat})
	})
	ensureRuntimeClass(t, env.c, cfg.Defaults.RuntimeClassName)

	t.Run("RendersTemplateAndPool", env.testRendersTemplateAndPool)
	t.Run("ReplicasUpdatePool", env.testReplicasUpdatePool)
	t.Run("AllModelsPricedReady", env.testAllModelsPricedReady)
	t.Run("UnknownModel", env.testUnknownModel)
	t.Run("UnpricedModel", env.testUnpricedModel)
	t.Run("GatewayUnavailable", env.testGatewayUnavailable)
	t.Run("PoolNotStarting", env.testPoolNotStarting)
	t.Run("AgentSandboxUnsupported", env.testAgentSandboxUnsupported)
}

func (e *profileEnv) testRendersTemplateAndPool(t *testing.T) {
	p := e.create(t, "render", 2, "priced")

	var tmpl extv1beta1.SandboxTemplate
	e.waitFor(t, "template", func() bool {
		return e.c.Get(t.Context(), e.key(p), &tmpl) == nil
	})
	var pool extv1beta1.SandboxWarmPool
	e.waitFor(t, "pool", func() bool {
		return e.c.Get(t.Context(), e.key(p), &pool) == nil
	})

	if got := ptr.Deref(pool.Spec.Replicas, -1); got != 2 {
		t.Errorf("pool replicas = %d, want 2", got)
	}
	if pool.Spec.TemplateRef.Name != sandbox.Name(p.Name) {
		t.Errorf("pool templateRef = %q, want %q", pool.Spec.TemplateRef.Name, sandbox.Name(p.Name))
	}
	if tmpl.Labels[sandbox.LabelProfile] != p.Name {
		t.Errorf("template label %s = %q, want %q", sandbox.LabelProfile, tmpl.Labels[sandbox.LabelProfile], p.Name)
	}
	for kind, obj := range map[string]client.Object{"template": &tmpl, "pool": &pool} {
		profileCheckOwner(t, kind, obj, p)
		profileCheckApplied(t, kind, obj)
	}

	var templates extv1beta1.SandboxTemplateList
	if err := e.c.List(t.Context(), &templates, client.InNamespace(e.cfg.SandboxNamespace), client.MatchingLabels{sandbox.LabelProfile: p.Name}); err != nil {
		t.Fatalf("list templates: %v", err)
	}
	var pools extv1beta1.SandboxWarmPoolList
	if err := e.c.List(t.Context(), &pools, client.InNamespace(e.cfg.SandboxNamespace), client.MatchingLabels{sandbox.LabelProfile: p.Name}); err != nil {
		t.Fatalf("list pools: %v", err)
	}
	if len(templates.Items) != 1 || len(pools.Items) != 1 {
		t.Errorf("got %d templates and %d pools for profile %s, want 1 and 1", len(templates.Items), len(pools.Items), p.Name)
	}
}

func (e *profileEnv) testReplicasUpdatePool(t *testing.T) {
	p := e.create(t, "replicas", 1, "priced")
	e.waitPoolReplicas(t, p, 1)

	patch := client.MergeFrom(p.DeepCopy())
	p.Spec.WarmPool.Replicas = ptr.To[int32](3)
	if err := e.c.Patch(t.Context(), p, patch); err != nil {
		t.Fatalf("patch replicas: %v", err)
	}
	e.waitPoolReplicas(t, p, 3)
}

func (e *profileEnv) testAllModelsPricedReady(t *testing.T) {
	p := e.create(t, "ready", 1, "priced")
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)
}

func (e *profileEnv) testUnknownModel(t *testing.T) {
	p := e.create(t, "unknown", 1, "priced", "missing")
	e.waitReady(t, p.Name, metav1.ConditionFalse, v1alpha1.ReasonModelUnknown)
}

func (e *profileEnv) testUnpricedModel(t *testing.T) {
	p := e.create(t, "unpriced", 1, "priced", "unpriced")
	e.waitReady(t, p.Name, metav1.ConditionFalse, v1alpha1.ReasonModelUnpriced)
}

func (e *profileEnv) testGatewayUnavailable(t *testing.T) {
	p := e.create(t, "gateway", 1, "priced")
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)

	e.gw.Unavailable(true)
	t.Cleanup(func() { e.gw.Unavailable(false) })
	e.poke(t, p.Name)
	e.waitReady(t, p.Name, metav1.ConditionFalse, v1alpha1.ReasonGatewayUnavailable)

	e.gw.Unavailable(false)
	e.poke(t, p.Name)
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)
}

func (e *profileEnv) testPoolNotStarting(t *testing.T) {
	p := e.create(t, "stalled", 1, "priced")
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)

	e.clock.Step(5 * time.Minute)
	e.poke(t, p.Name)
	e.waitReady(t, p.Name, metav1.ConditionFalse, v1alpha1.ReasonPoolNotStarting)

	if err := e.c.Get(t.Context(), client.ObjectKeyFromObject(p), p); err != nil {
		t.Fatalf("get profile: %v", err)
	}
	patch := client.MergeFrom(p.DeepCopy())
	p.Spec.Image = "example.com/agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	if err := e.c.Patch(t.Context(), p, patch); err != nil {
		t.Fatalf("patch image: %v", err)
	}
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)

	e.clock.Step(4 * time.Minute)
	e.poke(t, p.Name)
	e.clock.Step(time.Minute)
	e.poke(t, p.Name)
	e.waitReady(t, p.Name, metav1.ConditionFalse, v1alpha1.ReasonPoolNotStarting)

	e.binder.SetPoolReady(e.key(p), 1)
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)
}

func (e *profileEnv) testAgentSandboxUnsupported(t *testing.T) {
	p := e.create(t, "compat", 1, "priced")
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)

	supported := compat.Compat{State: compat.CompatSupported, Versions: []string{"v1.0.9"}, Message: "v1.0.9 supported"}
	t.Cleanup(func() { e.compat.Observe(context.Background(), supported) })
	unsupported := compat.Compat{State: compat.CompatUnsupported, Versions: []string{"v1.0.8"}, Message: "agent-sandbox controller versions v1.0.8 found"}
	e.compat.Observe(t.Context(), unsupported)
	e.waitReady(t, p.Name, metav1.ConditionFalse, v1alpha1.ReasonAgentSandboxUnsupported)
	var got v1alpha1.AgentProfile
	if err := e.c.Get(t.Context(), types.NamespacedName{Name: p.Name}, &got); err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionReady); c == nil || c.Message != unsupported.Message {
		t.Errorf("Ready condition = %+v, want message %q", c, unsupported.Message)
	}

	e.compat.Observe(t.Context(), compat.Compat{State: compat.CompatUnknown, Message: "list forbidden"})
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)

	e.compat.Observe(t.Context(), unsupported)
	e.waitReady(t, p.Name, metav1.ConditionFalse, v1alpha1.ReasonAgentSandboxUnsupported)
	e.compat.Observe(t.Context(), supported)
	e.waitReady(t, p.Name, metav1.ConditionTrue, v1alpha1.ReasonReady)
}

func (e *profileEnv) create(t *testing.T, name string, replicas int32, models ...string) *v1alpha1.AgentProfile {
	t.Helper()
	p := &v1alpha1.AgentProfile{
		ObjectMeta: testProfileMeta(e.cfg, name),
		Spec: v1alpha1.AgentProfileSpec{
			Image:    "example.com/agent@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Command:  []string{"omp", "--mode", "rpc"},
			Models:   models,
			WarmPool: v1alpha1.WarmPool{Replicas: ptr.To(replicas)},
		},
	}
	if err := e.c.Create(t.Context(), p); err != nil {
		t.Fatalf("create profile %s: %v", name, err)
	}
	return p
}

func (e *profileEnv) key(p *v1alpha1.AgentProfile) client.ObjectKey {
	return client.ObjectKey{Namespace: e.cfg.SandboxNamespace, Name: sandbox.Name(p.Name)}
}

func (e *profileEnv) poke(t *testing.T, name string) {
	t.Helper()
	data := []byte(`{"metadata":{"annotations":{"skua.dev/test-poke":"` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}}}`)
	p := &v1alpha1.AgentProfile{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := e.c.Patch(t.Context(), p, client.RawPatch(types.MergePatchType, data)); err != nil {
		t.Fatalf("annotate profile %s: %v", name, err)
	}
}

func (e *profileEnv) waitPoolReplicas(t *testing.T, p *v1alpha1.AgentProfile, want int32) {
	t.Helper()
	var pool extv1beta1.SandboxWarmPool
	e.waitFor(t, "pool replicas "+strconv.Itoa(int(want)), func() bool {
		return e.c.Get(t.Context(), e.key(p), &pool) == nil && ptr.Deref(pool.Spec.Replicas, -1) == want
	})
}

func (e *profileEnv) waitReady(t *testing.T, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	var last *metav1.Condition
	deadline := time.Now().Add(profileWaitTimeout)
	for time.Now().Before(deadline) {
		var p v1alpha1.AgentProfile
		if err := e.c.Get(t.Context(), client.ObjectKey{Name: name}, &p); err != nil {
			t.Fatalf("get profile %s: %v", name, err)
		}
		last = meta.FindStatusCondition(p.Status.Conditions, v1alpha1.ConditionReady)
		if last != nil && last.Status == status && last.Reason == reason &&
			last.ObservedGeneration == p.Generation && p.Status.ObservedGeneration == p.Generation {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("profile %s Ready = %+v, want %s/%s", name, last, status, reason)
}

func (e *profileEnv) waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(profileWaitTimeout)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func profileCheckOwner(t *testing.T, kind string, obj client.Object, p *v1alpha1.AgentProfile) {
	t.Helper()
	refs := obj.GetOwnerReferences()
	if len(refs) != 1 {
		t.Fatalf("%s owner references = %+v, want one", kind, refs)
	}
	ref := refs[0]
	if ref.APIVersion != v1alpha1.GroupVersion.String() || ref.Kind != "AgentProfile" || ref.Name != p.Name || ref.UID != p.UID || !ptr.Deref(ref.Controller, false) {
		t.Errorf("%s owner reference = %+v, want controller AgentProfile %s uid %s", kind, ref, p.Name, p.UID)
	}
}

func profileCheckApplied(t *testing.T, kind string, obj client.Object) {
	t.Helper()
	for _, f := range obj.GetManagedFields() {
		if f.Manager == opprofile.FieldManager && f.Operation == metav1.ManagedFieldsOperationApply {
			return
		}
	}
	t.Errorf("%s has no server-side apply entry by %s: %+v", kind, opprofile.FieldManager, obj.GetManagedFields())
}
