//go:build install

package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"

	"github.com/Devcon4/skua/test/kindenv"
)

const pullAttempts = 10

type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func helmBin() string { return envOr("HELM", "helm") }

func fluxBin() string { return envOr("FLUX", "flux") }

func keep() bool { return os.Getenv("KEEP") == "1" }

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s not found on PATH", tool)
		}
	}
}

func run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func mustRun(t *testing.T, stdin []byte, name string, args ...string) []byte {
	t.Helper()
	out, err := run(t.Context(), stdin, name, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func check(t *testing.T, desc string, ok bool) {
	t.Helper()
	if ok {
		t.Logf("PASS %s", desc)
	} else {
		t.Errorf("FAIL %s", desc)
	}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func cacheDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("SKUA_KIND_CACHE")
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(base, "skua-kind")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func dockerPull(t *testing.T, image string) {
	t.Helper()
	if _, err := run(t.Context(), nil, "docker", "image", "inspect", image); err == nil {
		return
	}
	for attempt := 1; ; attempt++ {
		_, err := run(t.Context(), nil, "docker", "pull", "-q", image)
		if err == nil {
			return
		}
		if attempt == pullAttempts {
			t.Fatalf("pulling %s failed %d times: %v", image, pullAttempts, err)
		}
		t.Logf("pull of %s failed (attempt %d), retrying: %v", image, attempt, err)
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(min(time.Duration(attempt)*10*time.Second, time.Minute)):
		}
	}
}

type kube struct {
	t          *testing.T
	kubeconfig string
	cluster    *kindenv.Cluster
}

func newKube(t *testing.T, name, nodeImage string, config *v1alpha4.Cluster) *kube {
	t.Helper()
	kubeconfig := "/tmp/" + name + ".kubeconfig"
	cluster, err := kindenv.NewCluster(name, kubeconfig, logWriter{t})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if keep() {
			t.Logf("KEEP=1: leaving cluster %s up (kubeconfig %s)", name, kubeconfig)
			return
		}
		if err := cluster.Delete(); err != nil {
			t.Error(err)
		}
	})
	if err := cluster.Create(nodeImage, config); err != nil {
		t.Fatal(err)
	}
	return &kube{t: t, kubeconfig: kubeconfig, cluster: cluster}
}

func (k *kube) kubectl(args ...string) ([]byte, error) {
	return run(k.t.Context(), nil, "kubectl", append([]string{"--kubeconfig", k.kubeconfig}, args...)...)
}

func (k *kube) must(args ...string) []byte {
	k.t.Helper()
	return mustRun(k.t, nil, "kubectl", append([]string{"--kubeconfig", k.kubeconfig}, args...)...)
}

func (k *kube) apply(manifest []byte) {
	k.t.Helper()
	mustRun(k.t, manifest, "kubectl", "--kubeconfig", k.kubeconfig, "apply", "-f", "-")
}

func (k *kube) applyObject(obj any) {
	k.t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		k.t.Fatal(err)
	}
	k.apply(raw)
}

func (k *kube) get(into any, args ...string) {
	k.t.Helper()
	out := k.must(append(append([]string{"get"}, args...), "-o", "json")...)
	if err := json.Unmarshal(out, into); err != nil {
		k.t.Fatalf("kubectl get %s: %v", strings.Join(args, " "), err)
	}
}

func (k *kube) helm(args ...string) []byte {
	k.t.Helper()
	return mustRun(k.t, nil, helmBin(), append([]string{"--kubeconfig", k.kubeconfig}, args...)...)
}

func (k *kube) pull(images ...string) {
	k.t.Helper()
	if err := k.cluster.Pull(k.t.Context(), images...); err != nil {
		k.t.Fatal(err)
	}
}

func (k *kube) pullDeployments(manifest []byte) {
	k.t.Helper()
	if err := k.cluster.PullDeployments(k.t.Context(), manifest); err != nil {
		k.t.Fatal(err)
	}
}

func (k *kube) crdMaxItems() string {
	k.t.Helper()
	var crd apiextensionsv1.CustomResourceDefinition
	k.get(&crd, "crd", "sandboxes.agents.x-k8s.io")
	if len(crd.Spec.Versions) == 0 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		return ""
	}
	maxItems := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["volumeClaimTemplates"].MaxItems
	if maxItems == nil {
		return ""
	}
	return strconv.FormatInt(*maxItems, 10)
}

func (k *kube) controllerImage() string {
	k.t.Helper()
	var deployments appsv1.DeploymentList
	k.get(&deployments, "deployment", "-n", "agent-sandbox-system")
	if len(deployments.Items) == 0 || len(deployments.Items[0].Spec.Template.Spec.Containers) == 0 {
		k.t.Fatal("no agent-sandbox controller Deployment in agent-sandbox-system")
	}
	return deployments.Items[0].Spec.Template.Spec.Containers[0].Image
}

func imageTag(image string) string {
	return image[strings.LastIndex(image, ":")+1:]
}
