package gitcred

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "gh":
		os.Exit(GhShim(os.Args[1:], time.Now()))
	case "git-credential-skua":
		os.Exit(Helper(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, time.Now()))
	}
	// Race-built children otherwise sleep 1 s at exit.
	_ = os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	os.Exit(m.Run())
}

var testNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func writeTokenFile(t *testing.T, path, token string, expiresAt time.Time) {
	t.Helper()
	if err := WriteToken(path, Token{Token: token, ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("WriteToken: %v", err)
	}
}

const forgeRequestBody = "protocol=https\nhost=forge.example\n\n"

func runHelper(args ...string) (stdout, stderr string, code int) {
	return runHelperWith(forgeRequestBody, args...)
}

func runHelperWith(request string, args ...string) (stdout, stderr string, code int) {
	var out, errb bytes.Buffer
	code = Helper(args, strings.NewReader(request), &out, &errb, testNow)
	return out.String(), errb.String(), code
}

func tokenEnv(t *testing.T, forgeURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forge-token.json")
	t.Setenv(sandbox.EnvForgeTokenFile, path)
	t.Setenv(sandbox.EnvForgeGitURL, forgeURL)
	return path
}

func shimDir(t *testing.T, names ...string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, n := range names {
		if err := os.Symlink(self, filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWriteTokenIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge-token.json")
	want := Token{Token: "tokA", ExpiresAt: testNow.Add(time.Hour)}
	if err := WriteToken(path, want); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"token":"tokA","expires_at":"2026-11-03T13:00:00Z"}` {
		t.Errorf("file = %s", raw)
	}
	got, err := ReadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != want.Token || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ReadToken = %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir holds %d entries, want only the token file", len(entries))
	}
}

func TestReadTokenRejectsEmptyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge-token.json")
	if err := os.WriteFile(path, []byte(`{"token":"","expires_at":"2026-11-03T13:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(path); err == nil {
		t.Fatal("ReadToken accepted a file without a token")
	}
}

func TestHelperReturnsTokenWithMoreThanMinLife(t *testing.T) {
	path := tokenEnv(t, "https://forge.example")
	writeTokenFile(t, path, "tokA", testNow.Add(5*time.Minute+2*time.Second))
	out, errOut, code := runHelper("get")
	if code != 0 || out != "username=x-access-token\npassword=tokA\n" || errOut != "" {
		t.Fatalf("Helper = %d %q %q", code, out, errOut)
	}
}

func TestHelperRefusesUnderMinLife(t *testing.T) {
	path := tokenEnv(t, "https://forge.example")
	writeTokenFile(t, path, "tokA", testNow.Add(4*time.Minute+59*time.Second))
	out, errOut, code := runHelper("get")
	if code != 0 || out != "quit=1\n" {
		t.Fatalf("Helper = %d %q", code, out)
	}
	want := "skua: forge token expires at 2026-11-03T12:04:59Z (4m59s left, under 5m0s); waiting for refresh from skua-api\n"
	if errOut != want {
		t.Fatalf("stderr = %q, want %q", errOut, want)
	}
}

func TestHelperReadsRewrittenFile(t *testing.T) {
	path := tokenEnv(t, "https://forge.example")
	writeTokenFile(t, path, "tokA", testNow.Add(time.Hour))
	if out, _, _ := runHelper("get"); out != "username=x-access-token\npassword=tokA\n" {
		t.Fatalf("first call = %q", out)
	}
	writeTokenFile(t, path, "tokB", testNow.Add(time.Hour))
	if out, _, _ := runHelper("get"); out != "username=x-access-token\npassword=tokB\n" {
		t.Fatalf("second call = %q", out)
	}
}

func TestHelperMissingFile(t *testing.T) {
	tokenEnv(t, "https://forge.example")
	out, errOut, code := runHelper("get")
	if code != 0 || out != "quit=1\n" || !strings.HasPrefix(errOut, "skua: no forge token: ") {
		t.Fatalf("Helper = %d %q %q", code, out, errOut)
	}
}

func TestHelperUnsetEnv(t *testing.T) {
	tokenEnv(t, "https://forge.example")
	t.Setenv(sandbox.EnvForgeTokenFile, "")
	out, errOut, _ := runHelper("get")
	if out != "quit=1\n" || !strings.Contains(errOut, sandbox.EnvForgeTokenFile) {
		t.Fatalf("Helper = %q %q", out, errOut)
	}
}

func TestHelperAnswersOnlyGet(t *testing.T) {
	path := tokenEnv(t, "https://forge.example")
	writeTokenFile(t, path, "tokA", testNow.Add(time.Hour))
	for _, action := range []string{"store", "erase"} {
		if out, errOut, code := runHelper(action); code != 0 || out != "" || errOut != "" {
			t.Errorf("%s = %d %q %q", action, code, out, errOut)
		}
	}
	if out, _, code := runHelper(); code != 0 || out != "" {
		t.Errorf("no action = %d %q", code, out)
	}
}

func TestHelperAnswersOnlyTheForge(t *testing.T) {
	const token = "username=x-access-token\npassword=tokA\n"
	for _, tc := range []struct {
		name, forgeURL, request, want string
	}{
		{"forge host", "https://forge.example", forgeRequestBody, token},
		{"host case", "https://Forge.Example", forgeRequestBody, token},
		{"other host", "https://forge.example", "protocol=https\nhost=evil.example\n\n", ""},
		{"suffix host", "https://forge.example", "protocol=https\nhost=forge.example.evil\n\n", ""},
		{"other port", "https://forge.example:8443", forgeRequestBody, ""},
		{"forge port", "https://forge.example:8443", "protocol=https\nhost=forge.example:8443\n\n", token},
		{"other protocol", "https://forge.example", "protocol=http\nhost=forge.example\n\n", ""},
		{"no host", "https://forge.example", "protocol=https\n\n", ""},
		{"forge url unset", "", forgeRequestBody, ""},
		{"path under base", "https://forge.example/git", "protocol=https\nhost=forge.example\npath=git/acme/widget.git\n\n", token},
		{"path outside base", "https://forge.example/git", "protocol=https\nhost=forge.example\npath=gitx/acme/widget.git\n\n", ""},
		{"no path sent", "https://forge.example/git/", forgeRequestBody, token},
		{"path without base", "https://forge.example", "protocol=https\nhost=forge.example\npath=acme/widget.git\n\n", token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tokenEnv(t, tc.forgeURL)
			writeTokenFile(t, path, "tokA", testNow.Add(time.Hour))
			out, errOut, code := runHelperWith(tc.request, "get")
			if code != 0 || out != tc.want || errOut != "" {
				t.Fatalf("Helper = %d %q %q, want %q", code, out, errOut, tc.want)
			}
		})
	}
}

func ghEnv(path, tokenFile string) []string {
	return []string{"PATH=" + path, sandbox.EnvForgeTokenFile + "=" + tokenFile}
}

func TestGhShimSkipsItselfAndExecsNextGh(t *testing.T) {
	shim := shimDir(t, "gh")
	otherShim := shimDir(t, "gh")
	notExec := t.TempDir()
	if err := os.WriteFile(filepath.Join(notExec, "gh"), []byte("#!/bin/sh\necho wrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	realGh := t.TempDir()
	if err := os.WriteFile(filepath.Join(realGh, "gh"), []byte("#!/bin/sh\necho \"token=$GH_TOKEN args=$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "forge-token.json")
	writeTokenFile(t, tokenFile, "tokGH", time.Now().Add(time.Hour))
	cmd := exec.Command(filepath.Join(shim, "gh"), "api", "user")
	cmd.Env = append(ghEnv(strings.Join([]string{shim, otherShim, notExec, realGh}, string(os.PathListSeparator)), tokenFile), "GH_TOKEN=stale")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gh shim: %v: %s", err, out)
	}
	if string(out) != "token=tokGH args=api user\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestGhShimRefusesUnderMinLife(t *testing.T) {
	shim := shimDir(t, "gh")
	realGh := t.TempDir()
	if err := os.WriteFile(filepath.Join(realGh, "gh"), []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "forge-token.json")
	writeTokenFile(t, tokenFile, "tokGH", time.Now().Add(4*time.Minute))
	cmd := exec.Command(filepath.Join(shim, "gh"), "api", "user")
	cmd.Env = ghEnv(shim+string(os.PathListSeparator)+realGh, tokenFile)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 4 {
		t.Fatalf("gh shim: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "under 5m0s); waiting for refresh from skua-api") || strings.Contains(string(out), "ran") {
		t.Fatalf("output = %q", out)
	}
}

func TestGhShimWithoutOtherGh(t *testing.T) {
	shim := shimDir(t, "gh")
	tokenFile := filepath.Join(t.TempDir(), "forge-token.json")
	writeTokenFile(t, tokenFile, "tokGH", time.Now().Add(time.Hour))
	cmd := exec.Command(filepath.Join(shim, "gh"), "auth", "status")
	cmd.Env = ghEnv(shim, tokenFile)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 127 {
		t.Fatalf("gh shim: %v: %s", err, out)
	}
}

type gitForge struct {
	mu       sync.Mutex
	valid    string
	withAuth int
	backend  http.Handler
}

func (g *gitForge) accept(token string) {
	g.mu.Lock()
	g.valid = token
	g.mu.Unlock()
}

func (g *gitForge) authorized() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.withAuth
}

func (g *gitForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	valid := g.valid
	if r.Header.Get("Authorization") != "" {
		g.withAuth++
	}
	g.mu.Unlock()
	auth, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Basic ")
	got, _ := base64.StdEncoding.DecodeString(auth)
	if string(got) != "x-access-token:"+valid {
		w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	g.backend.ServeHTTP(w, r)
}

func gitIn(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestGitCloneThroughCredentialHelper(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not installed")
	}

	root := t.TempDir()
	seed := t.TempDir()
	seedEnv := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=seed", "GIT_AUTHOR_EMAIL=seed@skua.invalid",
		"GIT_COMMITTER_NAME=seed", "GIT_COMMITTER_EMAIL=seed@skua.invalid",
	}
	for _, step := range [][]string{
		{"init", "-q", "--bare", "-b", "main", filepath.Join(root, "repo.git")},
		{"init", "-q", "-b", "main", "."},
		{"commit", "-q", "--allow-empty", "-m", "seed"},
		{"push", "-q", filepath.Join(root, "repo.git"), "main"},
	} {
		if out, err := gitIn(t, seed, seedEnv, step...); err != nil {
			t.Fatalf("git %v: %v: %s", step, err, out)
		}
	}

	forge := &gitForge{backend: &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=x-access-token"},
	}}
	srv := httptest.NewServer(forge)
	defer srv.Close()

	home := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "forge-token.json")
	forgeEnv := []string{
		"PATH=" + shimDir(t, "git-credential-skua") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GORACE=" + os.Getenv("GORACE"),
		"GIT_TERMINAL_PROMPT=0",
		sandbox.EnvForgeTokenFile + "=" + tokenFile,
		sandbox.EnvForgeGitURL + "=" + srv.URL,
	}
	env := append(slices.Clone(forgeEnv), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential."+srv.URL+".helper", "GIT_CONFIG_VALUE_0=skua")
	unscoped := append(slices.Clone(forgeEnv), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=skua")
	work := t.TempDir()
	clone := filepath.Join(work, "clone")

	writeTokenFile(t, tokenFile, "tokA", time.Now().Add(time.Hour))
	forge.accept("tokA")
	if out, err := gitIn(t, work, env, "clone", "-q", srv.URL+"/repo.git", clone); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	if out, err := gitIn(t, clone, env, "log", "-1", "--format=%s"); err != nil || out != "seed\n" {
		t.Fatalf("log: %v: %q", err, out)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("git wrote %d entries to HOME", len(entries))
	}

	other := &gitForge{backend: forge.backend}
	other.accept("tokA")
	otherSrv := httptest.NewServer(other)
	defer otherSrv.Close()
	for name, e := range map[string][]string{"scoped key": env, "unscoped key": unscoped} {
		if out, err := gitIn(t, work, e, "clone", "-q", otherSrv.URL+"/repo.git", filepath.Join(work, "other-"+strings.ReplaceAll(name, " ", "-"))); err == nil {
			t.Errorf("%s: clone from another host succeeded: %s", name, out)
		}
	}
	if n := other.authorized(); n != 0 {
		t.Errorf("another host received %d requests with credentials", n)
	}

	writeTokenFile(t, tokenFile, "tokB", time.Now().Add(time.Hour))
	forge.accept("tokB")
	if out, err := gitIn(t, clone, env, "fetch", "-q", "origin"); err != nil {
		t.Fatalf("fetch after rewrite: %v: %s", err, out)
	}

	writeTokenFile(t, tokenFile, "tokB", time.Now().Add(4*time.Minute))
	out, err := gitIn(t, clone, env, "fetch", "-q", "origin")
	if err == nil {
		t.Fatal("fetch succeeded with 4 minutes left on the token")
	}
	if !strings.Contains(out, "under 5m0s); waiting for refresh from skua-api") {
		t.Fatalf("fetch output = %q", out)
	}
}
