package omprpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
)

func TestStartProtocolUnsupported(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"}, "--protocol-versions", "1")
	begin := time.Now()
	requireReason(t, h.start(), v1alpha1.ReasonAgentProtocolUnsupported)
	if d := time.Since(begin); d >= 10*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestStartExitBeforeReady(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"}, "--exit-before-ready")
	requireReason(t, h.start(), v1alpha1.ReasonAgentStartFailed)
	select {
	case err := <-h.a.Exited():
		if err == nil {
			t.Fatal("Exited sent nil for exit status 1")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Exited did not fire")
	}
}

func TestStartModelNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"})
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "models.yml"), []byte("providers:\n  skua-gateway:\n    models:\n      - id: e2e-other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.params.Env = append(h.params.Env, fakeOmpAgentDirEnv+"="+other)
	requireReason(t, h.start(), v1alpha1.ReasonModelNotFound)
}

func TestResumeOpensSessionWithoutModel(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"})
	h.mustStart()
	h.prompt("SCRIPT:reply hello")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exited := h.a.Exited()
	if err := h.a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit after stdin close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not exit after Stop")
	}

	h.params.Resume = true
	h.mustStart()
	opens := h.commands("open_session")
	if len(opens) != 2 {
		t.Fatalf("open_session sent %d times", len(opens))
	}
	if _, ok := opens[0]["provider"]; !ok {
		t.Fatalf("first open_session lacks a model: %v", opens[0])
	}
	_, hasProvider := opens[1]["provider"]
	_, hasModel := opens[1]["modelId"]
	if hasProvider || hasModel || opens[1]["sessionDir"] != h.params.SessionDir {
		t.Fatalf("resume open_session = %v", opens[1])
	}
	text, err := h.a.LastText(ctx)
	if err != nil || text != "hello" {
		t.Fatalf("LastText after resume = %q, %v", text, err)
	}
}
