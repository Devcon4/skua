package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const preReceiveHook = `#!/bin/sh
status=0
while read -r _ _ ref; do
	case "$ref" in
	refs/heads/skua/*) ;;
	*)
		echo "GH013: Repository rule violations found for $ref: only refs/heads/skua/* may be updated." >&2
		status=1
		;;
	esac
done
exit "$status"
`

func (f *forge) gitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + f.root, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=fakeforge", "GIT_AUTHOR_EMAIL=fakeforge@skua.invalid",
		"GIT_COMMITTER_NAME=fakeforge", "GIT_COMMITTER_EMAIL=fakeforge@skua.invalid",
	}
}

func (f *forge) git(stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = f.gitEnv()
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func (f *forge) repoDir(repo string) string {
	return filepath.Join(f.root, filepath.FromSlash(repo)+".git")
}

func (f *forge) seed(repo string) error {
	dir := f.repoDir(repo)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if _, err := f.git("", "init", "-q", "--bare", "-b", defaultBranch, dir); err != nil {
		return err
	}
	blob, err := f.git("# "+repo+"\n", "--git-dir", dir, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	tree, err := f.git("100644 blob "+blob+"\tREADME.md\n", "--git-dir", dir, "mktree")
	if err != nil {
		return err
	}
	commit, err := f.git("", "--git-dir", dir, "commit-tree", tree, "-m", "seed "+repo)
	if err != nil {
		return err
	}
	if _, err := f.git("", "--git-dir", dir, "update-ref", "refs/heads/"+defaultBranch, commit); err != nil {
		return err
	}
	if _, err := f.git("", "--git-dir", dir, "config", "http.receivepack", "true"); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "hooks", "pre-receive"), []byte(preReceiveHook), 0o755)
}

func (f *forge) refs(repo string) (map[string]string, error) {
	out, err := f.git("", "--git-dir", f.repoDir(repo), "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for line := range strings.Lines(out) {
		name, sha, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			refs[name] = sha
		}
	}
	return refs, nil
}

func (f *forge) branchSHA(repo, branch string) (string, bool) {
	sha, err := f.git("", "--git-dir", f.repoDir(repo), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	return sha, err == nil && sha != ""
}

func (f *forge) serveGit(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutSuffix(r.PathValue("repo"), ".git")
	repo := r.PathValue("owner") + "/" + name
	if !ok || !f.repos[repo] {
		http.NotFound(w, r)
		return
	}
	user, password, ok := r.BasicAuth()
	if !ok || user != gitUser {
		w.Header().Set("WWW-Authenticate", `Basic realm="fakeforge"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	rec, ok := f.liveToken(password)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="fakeforge"`)
		http.Error(w, "invalid, expired or revoked token", http.StatusUnauthorized)
		return
	}
	if rec.repo != repo {
		http.Error(w, "token is not valid for "+repo, http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost && r.PathValue("rest") == "git-receive-pack" {
		f.mu.Lock()
		rec.pushes++
		f.mu.Unlock()
	}
	f.backend.ServeHTTP(w, r)
}
