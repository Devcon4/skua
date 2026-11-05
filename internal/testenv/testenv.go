// Package testenv boots an envtest API server with the skua and agent-sandbox CRDs.
package testenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Devcon4/skua/internal/integration/kube"
)

const (
	skuaModule    = "github.com/Devcon4/skua"
	sandboxModule = "sigs.k8s.io/agent-sandbox"
)

// ErrNoAssets is returned by Boot when KUBEBUILDER_ASSETS is unset.
var ErrNoAssets = errors.New("KUBEBUILDER_ASSETS is unset; run `make envtest` and set it to the output of `bin/setup-envtest use -p path <ENVTEST_K8S_VERSION> --bin-dir bin`")

// Env is a running envtest API server and an admin client whose scheme knows the core, skua and
// agent-sandbox types.
type Env struct {
	Config *rest.Config
	Client client.Client
	env    *envtest.Environment
}

// Boot starts an API server with config/crd/bases and the agent-sandbox CRDs installed, for one
// package's TestMain. It returns ErrNoAssets when KUBEBUILDER_ASSETS is unset; a relative
// KUBEBUILDER_ASSETS is resolved against the repository root. The caller stops it with Stop.
func Boot() (*Env, error) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		return nil, ErrNoAssets
	}
	dirs, err := moduleDirs()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(assets) {
		if err := os.Setenv("KUBEBUILDER_ASSETS", filepath.Join(dirs[skuaModule], assets)); err != nil {
			return nil, fmt.Errorf("set KUBEBUILDER_ASSETS: %w", err)
		}
	}
	scheme, err := kube.Scheme()
	if err != nil {
		return nil, fmt.Errorf("build scheme: %w", err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(dirs[skuaModule], "config", "crd", "bases"),
			filepath.Join(dirs[sandboxModule], "helm", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}
	// The watch cache answers each CRD's first request with 429 Retry-After: 1 until it initializes.
	env.ControlPlane.GetAPIServer().Configure().Set("watch-cache", "false")
	cfg, err := env.Start()
	if err != nil {
		return nil, fmt.Errorf("start envtest: %w", err)
	}
	if err := backdateEstablished(cfg, env.CRDs); err != nil {
		return nil, errors.Join(err, env.Stop())
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create client: %w", err), env.Stop())
	}
	return &Env{Config: cfg, Client: c, env: env}, nil
}

// The apiserver sleeps 2s in every custom resource create while its CRD was Established under 2s ago.
func backdateEstablished(cfg *rest.Config, crds []*apiextensionsv1.CustomResourceDefinition) error {
	s := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return fmt.Errorf("build CRD scheme: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		return fmt.Errorf("create CRD client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	past := metav1.NewTime(time.Now().Add(-time.Minute))
	for _, crd := range crds {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cur apiextensionsv1.CustomResourceDefinition
			if err := c.Get(ctx, client.ObjectKeyFromObject(crd), &cur); err != nil {
				return err
			}
			for i := range cur.Status.Conditions {
				if cur.Status.Conditions[i].Type == apiextensionsv1.Established {
					cur.Status.Conditions[i].LastTransitionTime = past
				}
			}
			return c.Status().Update(ctx, &cur)
		})
		if err != nil {
			return fmt.Errorf("backdate CRD %s: %w", crd.Name, err)
		}
	}
	return nil
}

// Stop shuts the API server down.
func (e *Env) Stop() error {
	if err := e.env.Stop(); err != nil {
		return fmt.Errorf("stop envtest: %w", err)
	}
	return nil
}

// Start boots an Env for t alone and stops it on t.Cleanup; it skips t when KUBEBUILDER_ASSETS is unset.
func Start(t *testing.T) (*rest.Config, client.Client) {
	t.Helper()
	e, err := Boot()
	if errors.Is(err, ErrNoAssets) {
		t.Skip(err.Error())
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
	})
	return e.Config, e.Client
}

func moduleDirs() (map[string]string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Dir}}", skuaModule, sandboxModule).Output()
	if err != nil {
		return nil, fmt.Errorf("locate module directories: %w", err)
	}
	dirs := map[string]string{}
	for line := range strings.Lines(string(out)) {
		path, dir, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && dir != "" {
			dirs[path] = dir
		}
	}
	for _, m := range []string{skuaModule, sandboxModule} {
		if dirs[m] == "" {
			return nil, fmt.Errorf("module %s has no directory; run `go mod download %s`", m, m)
		}
	}
	return dirs, nil
}
