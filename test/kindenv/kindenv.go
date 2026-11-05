// Package kindenv creates and tears down the kind test environment: a kind cluster with
// gVisor, agent-sandbox, Dex and LiteLLM, as an installer would provide them.
package kindenv

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	kindcluster "sigs.k8s.io/kind/pkg/cluster"
	kindcmd "sigs.k8s.io/kind/pkg/cmd"
	dockerimage "sigs.k8s.io/kind/pkg/cmd/kind/load/docker-image"
)

//go:embed manifests/*.yaml
var manifests embed.FS

// Config selects the cluster and how Up installs gVisor.
type Config struct {
	// Name is the kind cluster name.
	Name string
	// Kubeconfig is the kubeconfig path Up writes and the CLIs use.
	Kubeconfig string
	// NodeImage overrides kind's default node image when not empty.
	NodeImage string
	// GVisor is auto, runsc or runc. auto maps RuntimeClass gvisor to runsc when a smoke pod
	// runs under it and to runc otherwise; runsc fails instead of falling back.
	GVisor string
	// GVisorPlatform is auto, kvm or systrap. auto picks kvm when the node has /dev/kvm.
	GVisorPlatform string
	// CacheDir holds the gVisor release and the agent-sandbox checkout between runs.
	CacheDir string
	// Out receives progress lines.
	Out io.Writer
}

// EnvFile is the KEY=VALUE file Up writes for the Makefile and the e2e suite.
func EnvFile(name string) string { return "/tmp/" + name + ".env" }

// StateFile is the JSON State file Up writes.
func StateFile(name string) string { return "/tmp/" + name + ".state.json" }

func (c Config) validate() error {
	switch c.GVisor {
	case "auto", "runsc", "runc":
	default:
		return fmt.Errorf("gvisor mode %q; allowed: auto, runsc, runc", c.GVisor)
	}
	switch c.GVisorPlatform {
	case "auto", "kvm", "systrap":
	default:
		return fmt.Errorf("gvisor platform %q; allowed: auto, kvm, systrap", c.GVisorPlatform)
	}
	if c.Name == "" || c.Kubeconfig == "" || c.CacheDir == "" {
		return errors.New("name, kubeconfig and cache dir are required")
	}
	return nil
}

type env struct {
	cfg      Config
	provider *kindcluster.Provider
	start    time.Time
}

func newEnv(cfg Config) (*env, error) {
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	runtime, err := kindcluster.DetectNodeProvider()
	if err != nil {
		return nil, fmt.Errorf("detect kind node provider: %w", err)
	}
	return &env{
		cfg:      cfg,
		provider: kindcluster.NewProvider(runtime, kindcluster.ProviderWithLogger(kindcmd.NewLogger())),
		start:    time.Now(),
	}, nil
}

func (e *env) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.cfg.Out, "[%4ds] %s\n", int(time.Since(e.start).Seconds()), fmt.Sprintf(format, args...))
}

func (e *env) exists() (bool, error) {
	names, err := e.provider.List()
	if err != nil {
		return false, fmt.Errorf("list kind clusters: %w", err)
	}
	return slices.Contains(names, e.cfg.Name), nil
}

func run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (e *env) kubectl(ctx context.Context, args ...string) ([]byte, error) {
	return run(ctx, nil, "kubectl", append([]string{"--kubeconfig", e.cfg.Kubeconfig}, args...)...)
}

func (e *env) kubectlIn(ctx context.Context, stdin []byte, args ...string) error {
	_, err := run(ctx, bytes.NewReader(stdin), "kubectl", append([]string{"--kubeconfig", e.cfg.Kubeconfig}, args...)...)
	return err
}

func (e *env) helm(ctx context.Context, args ...string) error {
	_, err := run(ctx, nil, "helm", append([]string{"--kubeconfig", e.cfg.Kubeconfig}, args...)...)
	return err
}

func docker(ctx context.Context, args ...string) (string, error) {
	out, err := run(ctx, nil, "docker", args...)
	return strings.TrimSpace(string(out)), err
}

func (e *env) applyObject(ctx context.Context, obj any) error {
	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return e.kubectlIn(ctx, raw, "apply", "-f", "-")
}

func (e *env) createObject(ctx context.Context, obj any) error {
	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return e.kubectlIn(ctx, raw, "create", "-f", "-")
}

func (e *env) applyManifest(ctx context.Context, name string) error {
	raw, err := manifests.ReadFile("manifests/" + name)
	if err != nil {
		return err
	}
	return e.kubectlIn(ctx, raw, "apply", "-f", "-")
}

func (e *env) getJSON(ctx context.Context, into any, args ...string) (bool, error) {
	out, err := e.kubectl(ctx, append(append([]string{"get"}, args...), "--ignore-not-found", "-o", "json")...)
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(out, into)
}

func (e *env) has(ctx context.Context, args ...string) (bool, error) {
	out, err := e.kubectl(ctx, append(append([]string{"get"}, args...), "--ignore-not-found", "-o", "name")...)
	return len(bytes.TrimSpace(out)) > 0, err
}

// Down deletes the cluster and the files Up wrote. It removes the external egress container
// once no kind cluster is left.
func Down(cfg Config) error {
	e, err := newEnv(cfg)
	if err != nil {
		return err
	}
	if err := e.provider.Delete(cfg.Name, cfg.Kubeconfig); err != nil {
		return fmt.Errorf("delete kind cluster %s: %w", cfg.Name, err)
	}
	for _, f := range []string{cfg.Kubeconfig, EnvFile(cfg.Name), StateFile(cfg.Name)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	names, err := e.provider.List()
	if err == nil && len(names) == 0 {
		_, _ = docker(context.Background(), "rm", "-f", ExternalContainer)
	}
	return nil
}

// Load copies local docker images into every node of the cluster, as `kind load docker-image` does.
func Load(name string, images ...string) error {
	cmd := dockerimage.NewCommand(kindcmd.NewLogger(), kindcmd.StandardIOStreams())
	cmd.SetArgs(append([]string{"--name", name}, images...))
	cmd.SilenceUsage = true
	return cmd.Execute()
}

// Logs exports the cluster's node logs into dir, as `kind export logs` does.
func Logs(name, dir string) error {
	runtime, err := kindcluster.DetectNodeProvider()
	if err != nil {
		return fmt.Errorf("detect kind node provider: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return kindcluster.NewProvider(runtime, kindcluster.ProviderWithLogger(kindcmd.NewLogger())).CollectLogs(name, abs)
}
