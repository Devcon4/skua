// Package gitcred serves the run's forge token to git, as the credential
// helper `skua`, and to gh, as a shim that sets GH_TOKEN.
package gitcred

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/renameio/v2"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

// MinLife is the shortest remaining lifetime a token may have to be handed out.
const MinLife = 5 * time.Minute

// Token is the forge token file content.
type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WriteToken replaces the file at path with t, mode 0600, atomically.
func WriteToken(path string, t Token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	pf, err := renameio.NewPendingFile(path, renameio.WithStaticPermissions(0o600), renameio.WithTempDir(filepath.Dir(path)))
	if err != nil {
		return err
	}
	defer func() { _ = pf.Cleanup() }()
	if _, err := io.Copy(pf, bytes.NewReader(b)); err != nil {
		return err
	}
	return pf.CloseAtomicallyReplace()
}

// ReadToken reads the token file at path; a file without a token is an error.
func ReadToken(path string) (Token, error) {
	var t Token
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("parse %s: %w", path, err)
	}
	if t.Token == "" {
		return t, fmt.Errorf("%s holds no token", path)
	}
	return t, nil
}

func currentToken(now time.Time) (Token, error) {
	path := os.Getenv(sandbox.EnvForgeTokenFile)
	if path == "" {
		return Token{}, fmt.Errorf("skua: no forge token: %s is not set", sandbox.EnvForgeTokenFile)
	}
	t, err := ReadToken(path)
	if err != nil {
		return Token{}, fmt.Errorf("skua: no forge token: %w", err)
	}
	if left := t.ExpiresAt.Sub(now); left < MinLife {
		return Token{}, fmt.Errorf("skua: forge token expires at %s (%s left, under %s); waiting for refresh from skua-api",
			t.ExpiresAt.UTC().Format(time.RFC3339), left.Round(time.Second), MinLife)
	}
	return t, nil
}

// Helper runs the git credential helper protocol. Only `get` answers, and only for a request whose protocol
// and host (and path, when git sends one) match SKUA_FORGE_GIT_URL; any other request gets no output. It
// prints the token from the file named by SKUA_FORGE_TOKEN_FILE, or `quit=1` with a reason on stderr when
// the file is missing or the token has under MinLife left. It returns the process exit code.
func Helper(args []string, stdin io.Reader, stdout, stderr io.Writer, now time.Time) int {
	req := map[string]string{}
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			req[k] = v
		}
	}
	if len(args) == 0 || args[0] != "get" || !forgeRequest(os.Getenv(sandbox.EnvForgeGitURL), req) {
		return 0
	}
	out := "quit=1\n"
	if t, err := currentToken(now); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
	} else {
		out = "username=x-access-token\npassword=" + t.Token + "\n"
	}
	if _, err := io.WriteString(stdout, out); err != nil {
		return 1
	}
	return 0
}

func forgeRequest(forgeURL string, req map[string]string) bool {
	u, err := url.Parse(forgeURL)
	if forgeURL == "" || err != nil || u.Host == "" {
		return false
	}
	if req["protocol"] != u.Scheme || !strings.EqualFold(req["host"], u.Host) {
		return false
	}
	path, sent := req["path"]
	base := strings.Trim(u.Path, "/")
	if !sent || base == "" {
		return true
	}
	return path == base || strings.HasPrefix(path, base+"/")
}

// GhShim execs the first gh on PATH other than itself with GH_TOKEN set to
// the current token. It returns 4 when the token is missing or has under
// MinLife left, and 127 when no other gh is found or the exec fails.
func GhShim(args []string, now time.Time) int {
	t, err := currentToken(now)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 4
	}
	var self os.FileInfo
	if exe, err := os.Executable(); err == nil {
		self, _ = os.Stat(exe)
	}
	gh := findGh(os.Getenv("PATH"), self)
	if gh == "" {
		fmt.Fprintln(os.Stderr, "skua: no gh found on PATH besides the shim")
		return 127
	}
	env := []string{"GH_TOKEN=" + t.Token, "GH_PROMPT_DISABLED=1"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GH_TOKEN=") && !strings.HasPrefix(kv, "GH_PROMPT_DISABLED=") {
			env = append(env, kv)
		}
	}
	err = syscall.Exec(gh, append([]string{"gh"}, args...), env)
	fmt.Fprintln(os.Stderr, "skua: exec gh:", err)
	return 127
}

func findGh(path string, self os.FileInfo) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, "gh")
		st, err := os.Stat(cand)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 || (self != nil && os.SameFile(st, self)) {
			continue
		}
		return cand
	}
	return ""
}
