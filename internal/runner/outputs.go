package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-github/v88/github"

	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// outputs reports the run's branch when it exists on the forge and the first pull request from it. Any
// failure leaves that output out.
func (r *runner) outputs(ctx context.Context, a wire.Assign) wire.Outputs {
	var out wire.Outputs
	if a.Workspace == nil {
		return out
	}
	if _, err := os.Stat(filepath.Join(r.l.repoDir(), ".git")); err != nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*commandTimeout)
	defer cancel()
	branch := "skua/" + a.Run
	refs, err := r.git(ctx, r.gitEnv(a), r.l.repoDir(), "ls-remote", "--heads", "origin", branch)
	switch {
	case err != nil:
		slog.Info("run branch not checked", "run", a.Run, "err", err)
	case strings.TrimSpace(refs) != "":
		out.Branch = branch
	}
	n, err := r.pullRequest(ctx, a.Workspace.Repo, branch)
	if err != nil {
		slog.Info("run pull request not found", "run", a.Run, "err", err)
		return out
	}
	out.PullRequest = n
	return out
}

func (r *runner) pullRequest(ctx context.Context, repo, branch string) (int64, error) {
	if r.s.forgeAPIURL == "" {
		return 0, fmt.Errorf("%s is not set", sandbox.EnvForgeAPIURL)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return 0, fmt.Errorf("repo %q is not owner/name", repo)
	}
	tok, err := r.forgeToken()
	if err != nil {
		return 0, err
	}
	gh, err := github.NewClient(github.WithHTTPClient(r.hc), github.WithAuthToken(tok.Token), github.WithURLs(&r.s.forgeAPIURL, nil))
	if err != nil {
		return 0, err
	}
	prs, _, err := gh.PullRequests.List(ctx, owner, name, &github.PullRequestListOptions{Head: owner + ":" + branch, State: "all"})
	if err != nil {
		return 0, fmt.Errorf("list pull requests: %w", err)
	}
	if len(prs) == 0 {
		return 0, errors.New("no pull request from " + branch)
	}
	return int64(prs[0].GetNumber()), nil
}
