package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/Devcon4/skua/internal/api/auth/authtest"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/runner"
)

type kubelet struct {
	t       *testing.T
	reader  client.Reader
	reviews *authtest.TokenReviews
	root    string

	mu         sync.Mutex
	closed     bool
	containers map[string]*container
	logs       []string
}

type container struct {
	key    client.ObjectKey
	uid    types.UID
	layout runner.Layout
	env    []string
	args   []string
	log    string

	mu      sync.Mutex
	cmd     *exec.Cmd
	removed bool
	done    chan struct{}
}

func startKubelet(t *testing.T, mgr ctrl.Manager, reviews *authtest.TokenReviews) *kubelet {
	t.Helper()
	k := &kubelet{
		t:          t,
		reader:     mgr.GetAPIReader(),
		reviews:    reviews,
		root:       t.TempDir(),
		containers: map[string]*container{},
	}
	err := ctrl.NewControllerManagedBy(mgr).
		Named("kubelet").
		For(&corev1.Pod{}).
		WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
		Complete(k)
	if err != nil {
		t.Fatalf("kubelet: register controller: %v", err)
	}
	dumpOnFailure(t, func() []string {
		k.mu.Lock()
		defer k.mu.Unlock()
		return slices.Clone(k.logs)
	})
	return k
}

func (k *kubelet) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pod corev1.Pod
	err := k.reader.Get(ctx, req.NamespacedName, &pod)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	live := err == nil && pod.DeletionTimestamp.IsZero() && pod.Status.Phase == corev1.PodRunning
	k.mu.Lock()
	defer k.mu.Unlock()
	c := k.containers[req.Name]
	if c != nil && (!live || c.uid != pod.UID) {
		c.remove()
		delete(k.containers, req.Name)
		c = nil
	}
	if !live || c != nil || k.closed {
		return ctrl.Result{}, nil
	}
	c, err = k.create(&pod)
	if err != nil {
		return ctrl.Result{}, err
	}
	k.containers[req.Name] = c
	return ctrl.Result{}, k.run(c)
}

func (k *kubelet) create(pod *corev1.Pod) (*container, error) {
	var agent *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == sandbox.AgentContainerName {
			agent = &pod.Spec.Containers[i]
		}
	}
	if agent == nil {
		return nil, fmt.Errorf("pod %s has no %s container", pod.Name, sandbox.AgentContainerName)
	}
	sep := slices.Index(agent.Command, "--")
	if sep < 0 {
		return nil, fmt.Errorf("pod %s: agent command %q has no --", pod.Name, agent.Command)
	}
	dir := filepath.Join(k.root, string(pod.UID))
	c := &container{
		key: client.ObjectKeyFromObject(pod),
		uid: pod.UID,
		layout: runner.Layout{
			BinDir:    binDir,
			RunDir:    filepath.Join(dir, "run"),
			WorkDir:   filepath.Join(dir, "work"),
			TokenPath: filepath.Join(dir, "token"),
		},
		args: agent.Command[sep:],
		log:  filepath.Join(dir, "runner.log"),
	}
	for _, d := range []string{c.layout.RunDir, c.layout.WorkDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	token := "token-" + string(pod.UID)
	if err := os.WriteFile(c.layout.TokenPath, []byte(token+"\n"), 0o600); err != nil {
		return nil, err
	}
	k.reviews.Add(token, authtest.Token{
		Audience:       sandbox.TokenAudience,
		Namespace:      pod.Namespace,
		ServiceAccount: sandbox.ServiceAccountName,
		PodName:        pod.Name,
		PodUID:         string(pod.UID),
	})
	c.env = []string{
		roleEnv + "=runner",
		binDirEnv + "=" + c.layout.BinDir,
		runDirEnv + "=" + c.layout.RunDir,
		workDirEnv + "=" + c.layout.WorkDir,
		tokenPathEnv + "=" + c.layout.TokenPath,
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir,
		"GORACE=" + os.Getenv("GORACE"),
	}
	for _, e := range agent.Env {
		c.env = append(c.env, e.Name+"="+e.Value)
	}
	k.logs = append(k.logs, c.log)
	return c, nil
}

func (k *kubelet) run(c *container) error {
	logf, err := os.OpenFile(c.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(testBinary, c.args...)
	cmd.Env = c.env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	_ = logf.Close()
	if err != nil {
		return fmt.Errorf("start runner for pod %s: %w", c.key.Name, err)
	}
	c.mu.Lock()
	c.cmd = cmd
	c.done = make(chan struct{})
	done := c.done
	c.mu.Unlock()
	go func() {
		err := cmd.Wait()
		killSession(cmd.Process.Pid)
		close(done)
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		k.restart(c, int32(code))
	}()
	return nil
}

func (k *kubelet) restart(c *container, exitCode int32) {
	k.mu.Lock()
	defer k.mu.Unlock()
	c.mu.Lock()
	removed := c.removed
	c.mu.Unlock()
	if removed || k.closed || k.containers[c.key.Name] != c {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gone := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pod corev1.Pod
		if err := env.Client.Get(ctx, c.key, &pod); err != nil {
			return err
		}
		if pod.UID != c.uid || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
			gone = true
			return nil
		}
		now := metav1.Now()
		for i := range pod.Status.ContainerStatuses {
			cs := &pod.Status.ContainerStatuses[i]
			if cs.Name != sandbox.AgentContainerName {
				continue
			}
			started := now
			if cs.State.Running != nil {
				started = cs.State.Running.StartedAt
			}
			cs.RestartCount++
			cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: exitCode, Reason: "Error", StartedAt: started, FinishedAt: now,
			}}
			cs.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}
		}
		return env.Client.Status().Update(ctx, &pod)
	})
	if apierrors.IsNotFound(err) || gone {
		return
	}
	if err != nil {
		k.t.Errorf("kubelet: record restart of pod %s: %v", c.key.Name, err)
		return
	}
	if err := k.run(c); err != nil {
		k.t.Errorf("kubelet: %v", err)
	}
}

func (k *kubelet) stop(pod string) {
	k.t.Helper()
	k.mu.Lock()
	c := k.containers[pod]
	k.mu.Unlock()
	if c == nil {
		k.t.Fatalf("kubelet: pod %s has no container", pod)
	}
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		k.t.Fatalf("kubelet: SIGTERM runner of pod %s: %v", pod, err)
	}
}

func (k *kubelet) layout(pod string) runner.Layout {
	k.t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	c := k.containers[pod]
	if c == nil {
		k.t.Fatalf("kubelet: pod %s has no container", pod)
	}
	return c.layout
}

func (k *kubelet) stopAll() {
	k.mu.Lock()
	k.closed = true
	cs := make([]*container, 0, len(k.containers))
	for _, c := range k.containers {
		c.remove()
		cs = append(cs, c)
	}
	k.mu.Unlock()
	for _, c := range cs {
		c.mu.Lock()
		done := c.done
		c.mu.Unlock()
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			k.t.Errorf("kubelet: runner of pod %s did not exit", c.key.Name)
		}
	}
}

func (c *container) remove() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed = true
	if c.cmd != nil {
		killSession(c.cmd.Process.Pid)
	}
}

func killSession(sid int) {
	for range 3 {
		pids := sessionPIDs(sid)
		if len(pids) == 0 {
			return
		}
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func sessionPIDs(sid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) > 3 && f[0] != "Z" && f[3] == strconv.Itoa(sid) {
			out = append(out, pid)
		}
	}
	return out
}
