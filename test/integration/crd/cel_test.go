package crd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/testenv"
)

func validProfile(name string) *v1alpha1.AgentProfile {
	return &v1alpha1.AgentProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.AgentProfileSpec{
			Image:   "example.com/agent@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Command: []string{"omp", "--mode", "rpc"},
			Models:  []string{"claude-sonnet"},
		},
	}
}

func validRun(name string) *v1alpha1.AgentRun {
	return &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.AgentRunSpec{
			Profile:   "generic",
			ClientKey: "key-" + name,
			Workspace: &v1alpha1.RunWorkspace{Repo: "acme/widgets", BaseRef: "main"},
			Task: v1alpha1.Task{
				Prompt: "fix the build",
				Schema: &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
			},
		},
	}
}

func requireInvalid(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected the API server to refuse the object with %q, got no error", want)
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected an Invalid error, got %v", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error to contain %q, got %v", want, err)
	}
}

func setStatus(ctx context.Context, t *testing.T, c client.Client, run *v1alpha1.AgentRun, phase v1alpha1.RunPhase, reason string) error {
	t.Helper()
	if err := c.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	run.Status.Phase = phase
	run.Status.Reason = reason
	return c.Status().Update(ctx, run)
}

func TestCEL(t *testing.T) {
	_, c := testenv.Start(t)
	ctx := t.Context()

	t.Run("RunSpecImmutable", func(t *testing.T) {
		run := validRun("immutable")
		if err := c.Create(ctx, run); err != nil {
			t.Fatalf("create run: %v", err)
		}
		run.Spec.Task.Prompt = "something else"
		requireInvalid(t, c.Update(ctx, run), "spec is immutable")
	})

	t.Run("RunSpecSchemaImmutable", func(t *testing.T) {
		run := validRun("schema-immutable")
		if err := c.Create(ctx, run); err != nil {
			t.Fatalf("create run: %v", err)
		}
		run.Spec.Task.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"string"}`)}
		requireInvalid(t, c.Update(ctx, run), "spec is immutable")
	})

	t.Run("NonTerminalPhaseChangePasses", func(t *testing.T) {
		run := validRun("nonterminal")
		if err := c.Create(ctx, run); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if err := setStatus(ctx, t, c, run, v1alpha1.PhasePending, ""); err != nil {
			t.Fatalf("set Pending: %v", err)
		}
		if err := setStatus(ctx, t, c, run, v1alpha1.PhaseRunning, ""); err != nil {
			t.Fatalf("set Running: %v", err)
		}
		if err := setStatus(ctx, t, c, run, v1alpha1.PhaseFailed, v1alpha1.ReasonCancelled); err != nil {
			t.Fatalf("set Failed: %v", err)
		}
	})

	t.Run("TerminalPhaseAndReasonFrozen", func(t *testing.T) {
		run := validRun("terminal")
		if err := c.Create(ctx, run); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if err := setStatus(ctx, t, c, run, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted); err != nil {
			t.Fatalf("set Succeeded: %v", err)
		}
		const frozen = "phase and reason are immutable once the phase is terminal"
		requireInvalid(t, setStatus(ctx, t, c, run, v1alpha1.PhaseFailed, v1alpha1.ReasonCompleted), frozen)
		requireInvalid(t, setStatus(ctx, t, c, run, v1alpha1.PhaseSucceeded, v1alpha1.ReasonMaxRuntime), frozen)
		requireInvalid(t, setStatus(ctx, t, c, run, v1alpha1.PhaseRunning, ""), frozen)
		if err := setStatus(ctx, t, c, run, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted); err != nil {
			t.Fatalf("same terminal phase and reason: %v", err)
		}
	})

	t.Run("WorkspaceRepoPattern", func(t *testing.T) {
		run := validRun("no-slash")
		run.Spec.Workspace.Repo = "no-slash"
		requireInvalid(t, c.Create(ctx, run), "spec.workspace.repo")
	})

	t.Run("ModelsEmpty", func(t *testing.T) {
		p := validProfile("no-models")
		p.Spec.Models = []string{}
		requireInvalid(t, c.Create(ctx, p), "spec.models")
	})

	t.Run("AdapterEnum", func(t *testing.T) {
		p := validProfile("acp")
		p.Spec.Adapter = "acp"
		requireInvalid(t, c.Create(ctx, p), "spec.adapter")
	})

	t.Run("MaxResultBytesRange", func(t *testing.T) {
		p := validProfile("big-result")
		p.Spec.Limits.MaxResultBytes = 2000000
		requireInvalid(t, c.Create(ctx, p), "spec.limits.maxResultBytes")
	})

	t.Run("ProfileNameLength", func(t *testing.T) {
		p := validProfile(strings.Repeat("a", 51))
		requireInvalid(t, c.Create(ctx, p), "metadata.name must be at most 50 characters")
	})

	t.Run("ProfileDefaults", func(t *testing.T) {
		p := validProfile("defaults")
		if err := c.Create(ctx, p); err != nil {
			t.Fatalf("create profile: %v", err)
		}
		got := &v1alpha1.AgentProfile{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(p), got); err != nil {
			t.Fatalf("get profile: %v", err)
		}
		if r := got.Spec.WarmPool.Replicas; r == nil || *r != 1 {
			t.Errorf("warmPool.replicas = %v, want 1", r)
		}
		if d := got.Spec.Limits.MaxRuntime.Duration; d != 4*time.Hour {
			t.Errorf("limits.maxRuntime = %v, want 4h", d)
		}
		if got.Spec.Forge != v1alpha1.ForgeNone {
			t.Errorf("forge = %q, want %q", got.Spec.Forge, v1alpha1.ForgeNone)
		}
		if got.Spec.Adapter != v1alpha1.AdapterOmpRPC {
			t.Errorf("adapter = %q, want %q", got.Spec.Adapter, v1alpha1.AdapterOmpRPC)
		}
		if r := got.Spec.Limits.MaxRestarts; r == nil || *r != 3 {
			t.Errorf("limits.maxRestarts = %v, want 3", r)
		}
		if got.Spec.Limits.MaxResultBytes != 262144 {
			t.Errorf("limits.maxResultBytes = %d, want 262144", got.Spec.Limits.MaxResultBytes)
		}
		if s := got.Spec.Workspace.Size.String(); s != "10Gi" {
			t.Errorf("workspace.size = %s, want 10Gi", s)
		}
		if b := got.Spec.Limits.BudgetUSD.String(); b != "10" {
			t.Errorf("limits.budgetUSD = %s, want 10", b)
		}
	})
}
