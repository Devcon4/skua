package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const skillDir = ".agents/skills/skua-run"

// writeSkill writes the skua-run skill into the repository and, in a git workspace, keeps it out of git
// through .git/info/exclude.
func writeSkill(repo, run string, maxResultBytes int, git bool) error {
	dir := filepath.Join(repo, skillDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillText(run, maxResultBytes, git)), 0o644); err != nil {
		return err
	}
	if !git {
		return nil
	}
	return excludeFromGit(repo, skillDir+"/")
}

func skillText(run string, maxResultBytes int, git bool) string {
	branch := "skua/" + run
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: skua-run\n")
	b.WriteString("description: How to deliver this skua run: the branch to work on, pushing, the pull request, the result size cap and submit_result.\n")
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# skua run %s\n\n", run)
	writeDeliveryRules(&b, branch, maxResultBytes, git)
	b.WriteString("- Finish by calling the `submit_result` tool with the result.\n")
	return b.String()
}

func writeDeliveryRules(b *strings.Builder, branch string, maxResultBytes int, git bool) {
	if !git {
		b.WriteString("- This run has no repository; this directory is scratch space.\n")
		fmt.Fprintf(b, "- The result may be at most %d bytes.\n", maxResultBytes)
		return
	}
	fmt.Fprintf(b, "- Work on branch `%s`; it is checked out in this repository.\n", branch)
	fmt.Fprintf(b, "- Push with `git push origin HEAD:%s`. Pushes to any other branch are refused.\n", branch)
	fmt.Fprintf(b, "- Open the pull request with `gh pr create --head %s`.\n", branch)
	fmt.Fprintf(b, "- The result may be at most %d bytes. Keep larger output in git on `%s` and name its path in the result.\n", maxResultBytes, branch)
}

func excludeFromGit(repo, pattern string) error {
	path := filepath.Join(repo, ".git", "info", "exclude")
	cur, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for line := range bytes.SplitSeq(cur, []byte("\n")) {
		if string(bytes.TrimSpace(line)) == pattern {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(cur) > 0 && cur[len(cur)-1] != '\n' {
		cur = append(cur, '\n')
	}
	return os.WriteFile(path, append(cur, pattern+"\n"...), 0o644)
}
