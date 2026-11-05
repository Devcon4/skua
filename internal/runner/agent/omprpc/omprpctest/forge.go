package omprpctest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

func runName() (string, error) {
	n := os.Getenv(sandbox.EnvRun)
	if n == "" {
		return "", errors.New(sandbox.EnvRun + " is not set")
	}
	return n, nil
}

func (r *run) git(args ...string) error {
	return r.bash("git "+strings.Join(args, " "), func(*toolCall) (string, error) {
		out, err := exec.CommandContext(r.ctx, "git", args...).CombinedOutput()
		return string(out), err
	})
}

func (r *run) gitPush(file string) error {
	name, err := runName()
	if err != nil {
		return err
	}
	content := fmt.Sprintf("written by fakeomp for %s at %s\n", name, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"add", "--", file},
		{"commit", "-m", "fakeomp: add " + file},
		{"push", "origin", "HEAD:skua/" + name},
	} {
		if err := r.git(args...); err != nil {
			return fmt.Errorf("git %s: %w", args[0], err)
		}
	}
	return r.text("pushed skua/" + name)
}

func (r *run) pushAndPR(args []string) error {
	if len(args) != 1 {
		return errors.New("push-and-pr needs <file>")
	}
	if err := r.gitPush(args[0]); err != nil {
		return err
	}
	name, _ := runName()
	repo := os.Getenv(sandbox.EnvRepo)
	apiURL := os.Getenv(sandbox.EnvForgeAPIURL)
	if repo == "" || apiURL == "" {
		return errors.New("push-and-pr needs " + sandbox.EnvRepo + " and " + sandbox.EnvForgeAPIURL)
	}
	tokenFile, err := os.ReadFile(os.Getenv(sandbox.EnvForgeTokenFile))
	if err != nil {
		return fmt.Errorf("forge token: %w", err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(tokenFile, &tok); err != nil {
		return fmt.Errorf("forge token: %w", err)
	}
	head := "skua/" + name
	body, _ := json.Marshal(map[string]string{"title": head, "head": head, "base": "main"})
	url := strings.TrimSuffix(apiURL, "/") + "/repos/" + repo + "/pulls"
	var number int
	err = r.bash("gh pr create --head "+head+" --base main", func(*toolCall) (string, error) {
		req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			return string(raw), fmt.Errorf("pull request create: HTTP %d", resp.StatusCode)
		}
		var pr struct {
			Number int `json:"number"`
		}
		if err := json.Unmarshal(raw, &pr); err != nil {
			return string(raw), err
		}
		number = pr.Number
		return string(raw), nil
	})
	if err != nil || r.ctx.Err() != nil {
		return err
	}
	return r.text(fmt.Sprintf("pr: %d", number))
}

func (r *run) gitloop(args []string) error {
	total, err := durArg(args, 0)
	if err != nil {
		return err
	}
	interval, err := durArg(args, 1)
	if err != nil {
		return err
	}
	name, err := runName()
	if err != nil {
		return err
	}
	st := r.state()
	if st.StartedAt == 0 {
		st.StartedAt = time.Now().UnixMilli()
	}
	deadline := time.UnixMilli(st.StartedAt).Add(total)
	for time.Now().Before(deadline) {
		st.Progress++
		fetchErr := r.git("fetch", "origin")
		commitErr := r.git("commit", "--allow-empty", "-m", fmt.Sprintf("gitloop %d", st.Progress))
		pushErr := r.git("push", "origin", "HEAD:skua/"+name)
		if r.ctx.Err() != nil {
			return nil
		}
		if fetchErr != nil || commitErr != nil || pushErr != nil {
			st.Failures++
		}
		if err := r.save(); err != nil {
			return err
		}
		if !r.wait(min(interval, time.Until(deadline))) {
			return nil
		}
	}
	return r.text(fmt.Sprintf("gitloop failures=%d attempts=%d", st.Failures, st.Progress))
}

func (r *run) pushMain() error {
	if err := r.git("commit", "--allow-empty", "-m", "fakeomp: push-main"); err != nil {
		return err
	}
	code := 0
	err := r.git("push", "origin", "HEAD:main")
	if r.ctx.Err() != nil {
		return nil
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		return err
	}
	return r.text(fmt.Sprintf("push-main: %d", code))
}
