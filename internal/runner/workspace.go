package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/renameio/v2"

	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
	"github.com/Devcon4/skua/internal/runner/proc"
)

// prepare lays out /work for a fresh assignment: the agent's home, config and session dirs, the forge token
// file, the result schema, the repository on branch skua/<run> (or an empty dir) and the skua-run skill.
func (r *runner) prepare(ctx context.Context, a wire.Assign) error {
	for _, dir := range []string{r.l.homeDir(), r.l.agentDir(), r.l.sessionDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if a.Forge != nil {
		if err := r.writeForgeToken(a.Forge.Token, a.Forge.ExpiresAt); err != nil {
			return fmt.Errorf("write forge token: %w", err)
		}
	}
	if err := r.writeSchema(a.Task.Schema); err != nil {
		return fmt.Errorf("write result schema: %w", err)
	}
	repo := r.l.repoDir()
	if err := os.RemoveAll(repo); err != nil {
		return err
	}
	if a.Workspace == nil {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			return err
		}
	} else if err := r.clone(ctx, a); err != nil {
		return err
	}
	return writeSkill(repo, a.Run, r.s.maxResultBytes, a.Workspace != nil)
}

func (r *runner) writeSchema(schema []byte) error {
	if len(schema) == 0 {
		if err := os.Remove(r.l.schemaPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	pf, err := renameio.NewPendingFile(r.l.schemaPath(), renameio.WithStaticPermissions(0o644), renameio.WithTempDir(filepath.Dir(r.l.schemaPath())))
	if err != nil {
		return err
	}
	defer func() { _ = pf.Cleanup() }()
	if _, err := io.Copy(pf, bytes.NewReader(schema)); err != nil {
		return err
	}
	return pf.CloseAtomicallyReplace()
}

func (r *runner) clone(ctx context.Context, a wire.Assign) error {
	if r.s.forgeGitURL == "" {
		return fmt.Errorf("%s is not set", sandbox.EnvForgeGitURL)
	}
	src := strings.TrimSuffix(r.s.forgeGitURL, "/") + "/" + a.Workspace.Repo + ".git"
	env := r.gitEnv(a)
	if _, err := r.git(ctx, env, r.l.WorkDir, "clone", "--quiet", src, r.l.repoDir()); err != nil {
		return err
	}
	base, err := r.baseCommit(ctx, env, a.Workspace.BaseRef)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, env, r.l.repoDir(), "checkout", "--quiet", "--no-track", "-B", "skua/"+a.Run, base)
	return err
}

func (r *runner) baseCommit(ctx context.Context, env []string, ref string) (string, error) {
	candidates := []string{"HEAD"}
	if ref != "" {
		candidates = []string{"origin/" + ref, ref}
	}
	for _, c := range candidates {
		out, err := r.git(ctx, env, r.l.repoDir(), "rev-parse", "--verify", "--quiet", c+"^{commit}")
		if err == nil {
			return strings.TrimSpace(out), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("base ref %q not found", ref)
}

func (r *runner) gitEnv(a wire.Assign) []string {
	return append(r.agentEnv(a), "GIT_TERMINAL_PROMPT=0")
}

func (r *runner) git(ctx context.Context, env []string, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := proc.Run(ctx, cmd)
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(out))
	}
	return out, nil
}
