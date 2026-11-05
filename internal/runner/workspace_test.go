package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/agent"
)

func TestRunnerWorkspaceErrorOnMissingRepo(t *testing.T) {
	e := newRunnerEnv(t)
	f := newFakeAdapter()
	s := e.settings()
	s.forgeGitURL = "file://" + t.TempDir()
	e.start(f, s)
	a := plainAssign()
	a.Workspace = &wire.Workspace{Repo: "acme/missing", BaseRef: "main"}
	e.send(wire.CommandAssign, a)
	requireResult(t, e.result(), v1alpha1.PhaseFailed, v1alpha1.ReasonWorkspaceError)
	if n := len(f.snapshot().starts); n != 0 {
		t.Fatalf("starts = %d", n)
	}
}

type testForge struct {
	gitURL string
	api    *httptest.Server
	mu     sync.Mutex
	auth   []string
	heads  []string
}

func newTestForge(t *testing.T) *testForge {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "acme", "widget.git")
	src := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", "-b", "main", bare},
		{"-C", src, "init", "--quiet", "-b", "main"},
		{"-C", src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "init"},
		{"-C", src, "push", "--quiet", bare, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	f := &testForge{gitURL: "file://" + root}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.heads = append(f.heads, r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":7},{"number":3}]`))
	}))
	t.Cleanup(f.api.Close)
	return f
}

func workspaceAssign() wire.Assign {
	a := plainAssign()
	a.Workspace = &wire.Workspace{Repo: "acme/widget", BaseRef: "main"}
	a.Forge = &wire.Forge{Token: "tok-1", ExpiresAt: time.Now().Add(time.Hour)}
	return a
}

func gitIn(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunnerWorkspace(t *testing.T) {
	e := newRunnerEnv(t)
	forge := newTestForge(t)
	f := newFakeAdapter()
	f.setLast("pushed")
	f.onPrompt = func(f *fakeAdapter, _ context.Context, _ int, _ string) (agent.TurnResult, error) {
		p := f.snapshot().starts[0]
		cmd := exec.Command("git", "-C", p.WorkDir, "push", "--quiet", "origin", "HEAD:skua/"+testRun)
		cmd.Env = p.Env
		if out, err := cmd.CombinedOutput(); err != nil {
			return agent.TurnResult{}, fmt.Errorf("push: %w %s", err, out)
		}
		return agent.TurnResult{Status: "completed"}, nil
	}
	s := e.settings()
	s.forgeGitURL, s.forgeAPIURL = forge.gitURL, forge.api.URL
	e.start(f, s)
	e.send(wire.CommandAssign, workspaceAssign())
	res := e.result()
	requireResult(t, res, v1alpha1.PhaseSucceeded, v1alpha1.ReasonCompleted)
	if res.Outputs.Branch != "skua/"+testRun || res.Outputs.PullRequest != 7 {
		t.Fatalf("outputs = %+v", res.Outputs)
	}
	forge.mu.Lock()
	auth, heads := slices.Clone(forge.auth), slices.Clone(forge.heads)
	forge.mu.Unlock()
	if len(auth) != 1 || auth[0] != "Bearer tok-1" || heads[0] != "/repos/acme/widget/pulls?head=acme%3Askua%2F"+testRun+"&state=all" {
		t.Fatalf("pull request query: auth %q, url %q", auth, heads)
	}
	repo := e.l.repoDir()
	if _, err := os.Stat(filepath.Join(repo, skillDir, "SKILL.md")); err != nil {
		t.Fatalf("skill file: %v", err)
	}
	env := f.snapshot().starts[0].Env
	if got := gitIn(t, repo, env, "status", "--porcelain"); got != "" {
		t.Fatalf("git status --porcelain = %q", got)
	}
	if got := gitIn(t, repo, env, "rev-parse", "--abbrev-ref", "HEAD"); got != "skua/"+testRun {
		t.Fatalf("branch = %q", got)
	}
	if got, _ := envValue(env, "SKUA_REPO"); got != "acme/widget" {
		t.Fatalf("SKUA_REPO = %q", got)
	}
}
