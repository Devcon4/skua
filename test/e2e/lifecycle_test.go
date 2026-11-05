//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sandboxextv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

const (
	s27Profile    = "fake"
	s27User       = "dev@skua.test"
	s27RunTimeout = 5 * time.Minute
)

func TestEphemeralVolumeWorkspace(t *testing.T) {
	t.Parallel()
	c := s27Client(t)
	storageClass := "standard"
	profile := tempProfile(t, s27Profile, func(s *v1alpha1.AgentProfileSpec) {
		s.Workspace.StorageClassName = &storageClass
	})
	run := startRun(t, c, profile, "SCRIPT:reply ok", runOpts{})
	pvc := &corev1.PersistentVolumeClaim{}
	eventually(t, "workspace PVC of run "+run.Name, podTimeout, func(ctx context.Context) (bool, string) {
		var ar v1alpha1.AgentRun
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: runNamespace, Name: run.Name}, &ar); err != nil {
			return false, err.Error()
		}
		if ar.Status.PodName == "" {
			return false, "status.podName is empty"
		}
		name := ar.Status.PodName + "-" + sandbox.VolumeWorkspace
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: sandboxNamespace, Name: name}, pvc); err != nil {
			return false, err.Error()
		}
		if !slices.ContainsFunc(pvc.OwnerReferences, func(o metav1.OwnerReference) bool { return o.UID == ar.Status.PodUID }) {
			return false, fmt.Sprintf("PVC %s is not owned by pod uid %s", name, ar.Status.PodUID)
		}
		return true, ""
	})
	if sc := pvc.Spec.StorageClassName; sc == nil || *sc != storageClass {
		t.Errorf("PVC %s storageClassName = %v, want %q", pvc.Name, sc, storageClass)
	}
	waitPhase(t, run.Name, v1alpha1.PhaseSucceeded, "", s27RunTimeout)
	s27WaitGone(t, "claim "+run.Name, s27Claim(run.Name), podTimeout)
	s27WaitGone(t, "PVC "+pvc.Name, pvc, time.Minute)
}

func TestAgentCrashResume(t *testing.T) {
	t.Parallel()
	c := s27Client(t)
	run := startRun(t, c, s27Profile, "SCRIPT:orphan-then-crash", runOpts{})
	evs := events(t, c, run.Name)
	waitPhase(t, run.Name, v1alpha1.PhaseSucceeded, "", s27RunTimeout)
	texts := s27Texts(evs)
	if !slices.Contains(texts, "orphans: 0") {
		t.Errorf("text events = %q, want one equal to %q", texts, "orphans: 0")
	}
}

func TestNoZombies(t *testing.T) {
	t.Parallel()
	c := s27Client(t)
	run, pod := s27Running(t, c, s27Profile, "SCRIPT:steps 5 2s")
	stats, err := execIn(t, pod, "ps", "-eo", "stat=")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(stats) {
		if strings.HasPrefix(strings.TrimSpace(line), "Z") {
			t.Errorf("ps -eo stat= shows a zombie:\n%s", stats)
			break
		}
	}
	args, err := execIn(t, pod, "ps", "-o", "args=", "-p", "1")
	if err != nil {
		t.Fatal(err)
	}
	if f := strings.Fields(args); len(f) == 0 || path.Base(f[0]) != "skua-runner" {
		t.Errorf("PID 1 is %q, want skua-runner", strings.TrimSpace(args))
	}
	waitPhase(t, run.Name, v1alpha1.PhaseSucceeded, "", s27RunTimeout)
}

func s27Client(t *testing.T) apiClient {
	t.Helper()
	return apiClient{token: login(t, s27User)}
}

func s27Running(t *testing.T, c apiClient, profile, prompt string) (wire.Run, *corev1.Pod) {
	t.Helper()
	run := startRun(t, c, profile, prompt, runOpts{})
	waitPhase(t, run.Name, v1alpha1.PhaseRunning, "", podTimeout)
	return run, podOf(t, run.Name)
}

func s27Claim(run string) *sandboxextv1beta1.SandboxClaim {
	return &sandboxextv1beta1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{Namespace: sandboxNamespace, Name: run}}
}

func s27WaitGone(t *testing.T, what string, obj client.Object, timeout time.Duration) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	eventually(t, what+" gone", timeout, func(ctx context.Context) (bool, string) {
		err := k8s.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("still present, deletionTimestamp %v", obj.GetDeletionTimestamp())
	})
}

func s27Texts(evs []wire.LogEvent) []string {
	var out []string
	for _, ev := range evs {
		if ev.Type != wire.EventText {
			continue
		}
		var d wire.TextData
		if err := json.Unmarshal(ev.Data, &d); err == nil {
			out = append(out, d.Delta)
		}
	}
	return out
}
