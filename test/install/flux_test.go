//go:build install

package install

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	fluxRegistry = "skua-flux-registry"
	fleetRef     = "sourceRef: { kind: GitRepository, name: flux-system }"
	fleetOCIRef  = "sourceRef: { kind: OCIRepository, name: flux-system }"
	fleetPath    = "infrastructure/agent-sandbox"
)

var fluxObjects = []string{"kustomization/agent-sandbox-crds", "kustomization/agent-sandbox", "helmrelease/agent-sandbox"}

type fluxObject struct {
	Status struct {
		LastHandledReconcileAt string             `json:"lastHandledReconcileAt"`
		Conditions             []metav1.Condition `json:"conditions"`
	} `json:"status"`
}

type fluxEnv struct {
	*kube
	examples     string
	work         string
	registryPort string
}

func TestFluxExample(t *testing.T) {
	const name = "skua-flux"
	requireTools(t, "docker", "kubectl", fluxBin())
	examples, err := filepath.Abs(filepath.Join("..", "..", "docs", "install", "examples", "flux"))
	if err != nil {
		t.Fatal(err)
	}

	dockerPull(t, registryImage)
	_, _ = run(t.Context(), nil, "docker", "rm", "-f", fluxRegistry)
	nodeImage := envOr("SKUA_KIND_NODE_IMAGE", defaultNodeImage)
	t.Logf("creating kind cluster %s from %s", name, nodeImage)
	k := newKube(t, name, nodeImage, nil)

	t.Logf("starting OCI registry %s on the kind network for the fleet artifact", fluxRegistry)
	t.Cleanup(func() {
		if keep() {
			t.Logf("KEEP=1: leaving container %s up", fluxRegistry)
			return
		}
		if _, err := run(context.Background(), nil, "docker", "rm", "-f", fluxRegistry); err != nil {
			t.Error(err)
		}
	})
	mustRun(t, nil, "docker", "run", "-d", "--name", fluxRegistry, "--network", "kind", "-p", "127.0.0.1::5000", registryImage)
	port, ip := registryAddress(t)
	registryReady(t, port)

	version, err := run(t.Context(), nil, fluxBin(), "version", "--client")
	if err != nil {
		version = []byte(fluxBin())
	}
	t.Logf("installing Flux with %s", strings.TrimSpace(string(version)))
	k.pullDeployments(mustRun(t, nil, fluxBin(), "install", "--export"))
	mustRun(t, nil, fluxBin(), "--kubeconfig", k.kubeconfig, "install", "--timeout", "3m")
	k.applyObject(map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "OCIRepository",
		"metadata":   map[string]any{"name": "flux-system", "namespace": "flux-system"},
		"spec": map[string]any{
			"interval": "1m",
			"url":      "oci://" + ip + ":5000/skua-flux/fleet",
			"insecure": true,
			"ref":      map[string]any{"tag": "latest"},
		},
	})

	f := &fluxEnv{kube: k, examples: examples, work: t.TempDir(), registryPort: port}

	t.Logf("applying agent-sandbox.yaml rewritten to %s", oldAgentSandboxVersion)
	f.applyExample(oldAgentSandboxVersion)
	for _, obj := range fluxObjects {
		check(t, fmt.Sprintf("%s is Ready at %s", obj, oldAgentSandboxVersion), f.ready(obj))
	}
	oldImage := k.controllerImage()
	check(t, fmt.Sprintf("the controller runs %s (got %s)", oldAgentSandboxVersion, oldImage), imageTag(oldImage) == oldAgentSandboxVersion)

	t.Logf("applying agent-sandbox.yaml as written (%s)", newAgentSandboxVersion)
	f.applyExample(newAgentSandboxVersion)
	for _, obj := range fluxObjects {
		check(t, fmt.Sprintf("%s is Ready at %s", obj, newAgentSandboxVersion), f.ready(obj))
	}
	maxItems := k.crdMaxItems()
	check(t, fmt.Sprintf("after the upgrade the sandboxes CRD volumeClaimTemplates has maxItems: 128 (got '%s')", maxItems), maxItems == "128")
	image := k.controllerImage()
	check(t, fmt.Sprintf("the controller runs %s (got %s)", newAgentSandboxVersion, image), imageTag(image) == newAgentSandboxVersion)

	out, err := k.kubectl("apply", "--dry-run=server", "-f", filepath.Join(examples, "skua.yaml"))
	t.Log(strings.TrimSpace(string(out)))
	if err != nil {
		t.Log(err)
	}
	check(t, "kubectl apply --dry-run=server accepts skua.yaml against the Flux CRDs", err == nil)
}

func registryAddress(t *testing.T) (port, ip string) {
	t.Helper()
	var containers []struct {
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
			Ports    map[string][]struct{ HostIP, HostPort string }
		}
	}
	if err := json.Unmarshal(mustRun(t, nil, "docker", "inspect", fluxRegistry), &containers); err != nil {
		t.Fatalf("docker inspect %s: %v", fluxRegistry, err)
	}
	if len(containers) != 1 {
		t.Fatalf("docker inspect %s: want one container, got %d", fluxRegistry, len(containers))
	}
	settings := containers[0].NetworkSettings
	if bindings := settings.Ports["5000/tcp"]; len(bindings) > 0 {
		port = bindings[0].HostPort
	}
	ip = settings.Networks["kind"].IPAddress
	if port == "" || ip == "" {
		t.Fatalf("registry %s has host port %q and kind network address %q", fluxRegistry, port, ip)
	}
	return port, ip
}

func registryReady(t *testing.T, port string) {
	t.Helper()
	url := "http://127.0.0.1:" + port + "/v2/"
	client := &http.Client{Timeout: 5 * time.Second}
	ok := func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode < http.StatusBadRequest
	}
	for range 30 {
		if ok() {
			return
		}
		time.Sleep(time.Second)
	}
	if !ok() {
		t.Fatalf("registry %s did not answer on 127.0.0.1:%s", fluxRegistry, port)
	}
}

func (f *fluxEnv) applyExample(version string) {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.examples, "agent-sandbox.yaml"))
	if err != nil {
		f.t.Fatal(err)
	}
	release, cluster := splitHelmRelease(yamlDocs(strings.ReplaceAll(string(raw), newAgentSandboxVersion, version)))
	if release == "" {
		f.t.Fatal("agent-sandbox.yaml has no HelmRelease")
	}
	if fleetRefCount(cluster) != 1 {
		f.t.Fatalf("agent-sandbox.yaml must name the fleet source exactly once as '%s'", fleetRef)
	}

	fleet := filepath.Join(f.work, version, "fleet")
	if err := os.MkdirAll(filepath.Join(fleet, fleetPath), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fleet, fleetPath, "release.yaml"), []byte(release), 0o644); err != nil {
		f.t.Fatal(err)
	}
	sum := sha1.Sum([]byte(release))
	mustRun(f.t, nil, fluxBin(), "push", "artifact", "oci://127.0.0.1:"+f.registryPort+"/skua-flux/fleet:latest",
		"--path", fleet, "--source", "file://"+filepath.Join(f.examples, "agent-sandbox.yaml"),
		"--revision", version+"@sha1:"+hex.EncodeToString(sum[:]), "--insecure-registry")
	f.apply([]byte(strings.Replace(cluster, fleetRef, fleetOCIRef, 1)))

	f.reconcile("gitrepository/agent-sandbox", 60)
	f.reconcile("kustomization/agent-sandbox-crds", 60)
	f.reconcile("ocirepository/flux-system", 30)
	f.reconcile("kustomization/agent-sandbox", 180)
	f.reconcile("helmchart/flux-system-agent-sandbox", 60)
	f.reconcile("helmrelease/agent-sandbox", 120)
}

func yamlDocs(text string) [][]string {
	var docs [][]string
	var doc []string
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if strings.TrimSuffix(line, "\n") == "---" {
			docs, doc = append(docs, doc), nil
			continue
		}
		doc = append(doc, line)
	}
	return append(docs, doc)
}

func isHelmRelease(doc []string) bool {
	return slices.ContainsFunc(doc, func(line string) bool {
		return strings.TrimSuffix(line, "\n") == "kind: HelmRelease"
	})
}

func splitHelmRelease(docs [][]string) (release, cluster string) {
	var clusterDocs strings.Builder
	for _, d := range docs {
		if len(d) == 0 {
			continue
		}
		text := strings.Join(d, "")
		if isHelmRelease(d) {
			release = text
			continue
		}
		clusterDocs.WriteString("---\n")
		clusterDocs.WriteString(text)
	}
	return release, clusterDocs.String()
}

func fleetRefCount(cluster string) int {
	refs := 0
	for _, line := range strings.Split(cluster, "\n") {
		if strings.Contains(line, fleetRef) {
			refs++
		}
	}
	return refs
}

func (f *fluxEnv) status(obj string) (handled string, ready metav1.ConditionStatus) {
	f.t.Helper()
	var o fluxObject
	f.get(&o, obj, "-n", "flux-system")
	if c := apimeta.FindStatusCondition(o.Status.Conditions, "Ready"); c != nil {
		ready = c.Status
	}
	return o.Status.LastHandledReconcileAt, ready
}

func (f *fluxEnv) ready(obj string) bool {
	f.t.Helper()
	_, ready := f.status(obj)
	return ready == metav1.ConditionTrue
}

func (f *fluxEnv) reconcile(obj string, timeoutSeconds int) {
	f.t.Helper()
	token := strconv.FormatInt(time.Now().UnixNano(), 10)
	f.must("annotate", "-n", "flux-system", "--overwrite", obj, "reconcile.fluxcd.io/requestedAt="+token)
	start := time.Now()
	for time.Since(start) < time.Duration(timeoutSeconds)*time.Second {
		handled, ready := f.status(obj)
		if handled == token && ready == metav1.ConditionTrue {
			f.t.Logf("%s Ready after %ds", obj, int(time.Since(start).Seconds()))
			return
		}
		if handled == token && ready == metav1.ConditionFalse {
			break
		}
		time.Sleep(2 * time.Second)
	}
	f.diagnose(obj)
	f.t.Fatalf("FAIL %s reconciles to Ready within %ds", obj, timeoutSeconds)
}

func (f *fluxEnv) diagnose(obj string) {
	f.t.Helper()
	f.t.Logf("status of %s", obj)
	if out, err := f.kubectl("get", obj, "-n", "flux-system", "-o", "json"); err == nil {
		var o fluxObject
		if json.Unmarshal(out, &o) == nil {
			for _, c := range o.Status.Conditions {
				f.t.Logf("%s=%s %s: %s", c.Type, c.Status, c.Reason, c.Message)
			}
		}
	}
	f.t.Log("flux get all -n flux-system")
	out, err := run(f.t.Context(), nil, fluxBin(), "--kubeconfig", f.kubeconfig, "get", "all", "-n", "flux-system")
	f.logOutput(out, err)
	f.t.Log("events in flux-system")
	f.logEvents()
	for _, controller := range controllersOf(obj) {
		f.t.Logf("%s log tail", controller)
		out, err := f.kubectl("logs", "-n", "flux-system", "deploy/"+controller, "--tail=40")
		f.logOutput(out, err)
	}
}

func (f *fluxEnv) logEvents() {
	f.t.Helper()
	out, err := f.kubectl("get", "events", "-n", "flux-system", "--sort-by=.lastTimestamp", "-o", "json")
	if err != nil {
		f.t.Log(err)
		return
	}
	var events corev1.EventList
	if json.Unmarshal(out, &events) != nil {
		return
	}
	for _, e := range events.Items {
		f.t.Logf("%s %s %s/%s: %s", e.Type, e.Reason, e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Message)
	}
}

func (f *fluxEnv) logOutput(out []byte, err error) {
	if len(out) > 0 {
		f.t.Log(strings.TrimSpace(string(out)))
	}
	if err != nil {
		f.t.Log(err)
	}
}

func controllersOf(obj string) []string {
	switch {
	case obj == "kustomization/agent-sandbox":
		return []string{"kustomize-controller", "helm-controller"}
	case strings.HasPrefix(obj, "kustomization/"):
		return []string{"kustomize-controller"}
	case strings.HasPrefix(obj, "helmrelease/"):
		return []string{"helm-controller"}
	}
	return []string{"source-controller"}
}
