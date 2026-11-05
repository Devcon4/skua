// Package integration runs skua-operator, skua-api and skua-runner in one process tree against one envtest
// API server, with fakes standing in for agent-sandbox, the kubelet, LiteLLM, GitHub and omp.
package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Devcon4/skua/internal/runner"
	"github.com/Devcon4/skua/internal/runner/agent/omprpc"
	"github.com/Devcon4/skua/internal/runner/agent/omprpc/omprpctest"
	"github.com/Devcon4/skua/internal/runner/gitcred"
	"github.com/Devcon4/skua/internal/runner/mcp"
	"github.com/Devcon4/skua/internal/testenv"
	"github.com/Devcon4/skua/internal/version"
)

const (
	roleEnv      = "SKUA_IT_ROLE"
	binDirEnv    = "SKUA_IT_BIN_DIR"
	runDirEnv    = "SKUA_IT_RUN_DIR"
	workDirEnv   = "SKUA_IT_WORK_DIR"
	tokenPathEnv = "SKUA_IT_TOKEN_PATH"

	runnerVersion = "0.1.0"
	raceFastExit  = `GORACE="$GORACE atexit_sleep_ms=0"`
	runtimeClass  = "gvisor"
)

var (
	env        *testenv.Env
	testBinary string
	binDir     string
)

func TestMain(m *testing.M) {
	if role := os.Getenv(roleEnv); role != "" {
		os.Exit(runRole(role))
	}
	os.Exit(runTests(m))
}

func runRole(role string) int {
	switch role {
	case "fakeomp":
		return omprpctest.Main(os.Args[1:], os.Stdin, os.Stdout)
	case "skua-runner":
		if len(os.Args) != 2 || os.Args[1] != "mcp" {
			return 2
		}
		if err := mcp.Serve(context.Background(), os.Stdin, os.Stdout, os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "git-credential":
		return gitcred.Helper(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, time.Now())
	case "runner":
		version.Version = runnerVersion
		return runner.MainLayout(os.Args[1:], runner.Layout{
			BinDir:    os.Getenv(binDirEnv),
			RunDir:    os.Getenv(runDirEnv),
			WorkDir:   os.Getenv(workDirEnv),
			TokenPath: os.Getenv(tokenPathEnv),
		}, omprpc.New())
	}
	fmt.Fprintf(os.Stderr, "unknown %s %q\n", roleEnv, role)
	return 2
}

func runTests(m *testing.M) int {
	ctrl.SetLogger(logr.Discard())
	_ = os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	testBinary = exe
	tmp, err := os.MkdirTemp("", "skua-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	binDir = filepath.Join(tmp, "bin")
	if err := writeShims(binDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	env, err = testenv.Boot()
	if errors.Is(err, testenv.ErrNoAssets) {
		fmt.Fprintln(os.Stderr, "integration tests skip: "+err.Error())
		return m.Run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := 1
	rc := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: runtimeClass}, Handler: "runsc"}
	if err := env.Client.Create(context.Background(), rc); err != nil {
		fmt.Fprintf(os.Stderr, "create runtime class %s: %v\n", runtimeClass, err)
	} else {
		code = m.Run()
	}
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func writeShims(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, role := range map[string]string{
		"fakeomp":             "fakeomp",
		"skua-runner":         "skua-runner",
		"git-credential-skua": "git-credential",
	} {
		script := "#!/bin/sh\n" + raceFastExit + " " + roleEnv + "=" + role + " exec '" + testBinary + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func requireEnv(t *testing.T) {
	t.Helper()
	if env == nil {
		t.Skip(testenv.ErrNoAssets.Error())
	}
}
