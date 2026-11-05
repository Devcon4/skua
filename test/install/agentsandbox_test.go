//go:build install

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/Devcon4/skua/test/kindenv"
)

func TestAgentSandboxUpgrade(t *testing.T) {
	const name, ns = "skua-r2", "skua-r2"
	requireTools(t, "docker", "kubectl", "git", helmBin())
	cache := cacheDir(t)
	oldSrc := agentSandboxSource(t, cache, oldAgentSandboxVersion, oldAgentSandboxCommit)
	newSrc := agentSandboxSource(t, cache, newAgentSandboxVersion, newAgentSandboxCommit)

	nodeImage := envOr("SKUA_KIND_NODE_IMAGE", defaultNodeImage)
	t.Logf("creating kind cluster %s from %s", name, nodeImage)
	k := newKube(t, name, nodeImage, nil)
	k.pull(kindenv.BusyboxImage)
	for version, src := range map[string]string{oldAgentSandboxVersion: oldSrc, newAgentSandboxVersion: newSrc} {
		k.pullDeployments(k.helm(append([]string{"template", "agent-sandbox", filepath.Join(src, "helm"),
			"--namespace", "agent-sandbox-system", "--skip-crds"}, agentSandboxValues(version)...)...))
	}

	t.Logf("installing agent-sandbox %s", oldAgentSandboxVersion)
	k.installAgentSandbox(oldAgentSandboxVersion, oldSrc)
	t.Logf("agent-sandbox %s: controller %s, volumeClaimTemplates maxItems=%s", oldAgentSandboxVersion, k.controllerImage(), k.crdMaxItems())

	t.Logf("creating SandboxTemplate, SandboxWarmPool and SandboxClaim survivor in namespace %s", ns)
	k.must("create", "namespace", ns)
	k.apply([]byte(strings.ReplaceAll(string(readTestdata(t, "survivor.yaml")), "${BUSYBOX_IMAGE}", kindenv.BusyboxImage)))
	k.must("wait", "sandboxwarmpool/survivor", "-n", ns, "--for=jsonpath={.status.readyReplicas}=1", "--timeout=180s")
	k.apply(readTestdata(t, "survivor-claim.yaml"))
	k.must("wait", "sandboxclaim/survivor", "-n", ns, "--for=condition=Ready", "--timeout=120s")
	before := k.claimedPodUIDs(ns)
	if len(before) != 1 {
		t.Fatalf("expected one claimed pod before the upgrade, got: %v", before)
	}
	t.Logf("claim survivor Ready on pod uid %s", before[0])

	t.Logf("Helm-only upgrade to %s (chart upgrade without the CRD step)", newAgentSandboxVersion)
	k.helm("upgrade", "agent-sandbox", filepath.Join(newSrc, "helm"), "--namespace", "agent-sandbox-system",
		"--reuse-values", "--set", "image.tag="+newAgentSandboxVersion, "--wait", "--timeout", "3m")
	helmOnly := k.crdMaxItems()
	check(t, fmt.Sprintf("after a Helm-only upgrade the sandboxes CRD volumeClaimTemplates has no maxItems: 128 (got '%s')", helmOnly),
		helmOnly != "128")

	t.Logf("full upgrade to %s (server-side CRD apply, then helm upgrade --install)", newAgentSandboxVersion)
	k.installAgentSandbox(newAgentSandboxVersion, newSrc)
	full := k.crdMaxItems()
	check(t, fmt.Sprintf("after the CRD step and helm upgrade the sandboxes CRD volumeClaimTemplates has maxItems: 128 (got '%s')", full),
		full == "128")

	image := k.controllerImage()
	check(t, fmt.Sprintf("the controller image tag is %s (got %s)", newAgentSandboxVersion, image), imageTag(image) == newAgentSandboxVersion)

	_, err := k.kubectl("wait", "sandboxclaim/survivor", "-n", ns, "--for=condition=Ready", "--timeout=60s")
	claimReady := err == nil
	after := k.claimedPodUIDs(ns)
	check(t, fmt.Sprintf("claim survivor is Ready (%t) on the same pod uid (before %s, after %s)", claimReady, before[0], strings.Join(after, ",")),
		claimReady && slices.Equal(after, before))
}

func agentSandboxSource(t *testing.T, cache, version, commit string) string {
	t.Helper()
	dir := filepath.Join(cache, "agent-sandbox-"+version)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		mustRun(t, nil, "git", "-c", "advice.detachedHead=false", "clone", "-q", "--depth", "1", "--branch", version, agentSandboxRepository, dir)
	}
	if head := strings.TrimSpace(string(mustRun(t, nil, "git", "-C", dir, "rev-parse", "HEAD"))); head != commit {
		t.Fatalf("%s is not agent-sandbox %s at %s; delete it and rerun", dir, version, commit)
	}
	return dir
}

func agentSandboxValues(version string) []string {
	return []string{"--set", "namespace.create=false", "--set", "image.tag=" + version, "--set", "controller.extensions=true"}
}

func (k *kube) installAgentSandbox(version, src string) {
	k.t.Helper()
	crds := filepath.Join(src, "helm", "crds") + "/"
	k.must("apply", "--server-side", "--force-conflicts", "-f", crds)
	k.must("wait", "--for=condition=Established", "--timeout=60s", "-f", crds)
	k.helm(append([]string{"upgrade", "--install", "agent-sandbox", filepath.Join(src, "helm"),
		"--namespace", "agent-sandbox-system", "--create-namespace", "--skip-crds"},
		append(agentSandboxValues(version), "--wait", "--timeout", "3m")...)...)
}

func (k *kube) claimedPodUIDs(namespace string) []string {
	k.t.Helper()
	var pods corev1.PodList
	k.get(&pods, "pods", "-n", namespace, "-l", "agents.x-k8s.io/claim-uid")
	uids := make([]string, 0, len(pods.Items))
	for _, p := range pods.Items {
		uids = append(uids, string(p.UID))
	}
	return uids
}
