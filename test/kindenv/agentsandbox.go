package kindenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (e *env) installAgentSandbox(ctx context.Context) error {
	e.logf("installing agent-sandbox %s (CRDs server-side, chart with --skip-crds)", AgentSandboxVersion)
	src := filepath.Join(e.cfg.CacheDir, "agent-sandbox-"+AgentSandboxVersion)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		if _, err := run(ctx, nil, "git", "-c", "advice.detachedHead=false", "clone", "-q", "--depth", "1",
			"--branch", AgentSandboxVersion, agentSandboxRepository, src); err != nil {
			return err
		}
	}
	head, err := run(ctx, nil, "git", "-C", src, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != AgentSandboxCommit {
		return fmt.Errorf("%s is not agent-sandbox %s at %s; delete it and rerun", src, AgentSandboxVersion, AgentSandboxCommit)
	}
	crds := filepath.Join(src, "helm", "crds") + "/"
	if _, err := e.kubectl(ctx, "apply", "--server-side", "--force-conflicts", "-f", crds); err != nil {
		return err
	}
	if _, err := e.kubectl(ctx, "wait", "--for=condition=Established", "--timeout=60s", "-f", crds); err != nil {
		return err
	}
	return e.helm(ctx, "upgrade", "--install", "agent-sandbox", filepath.Join(src, "helm"),
		"--namespace", "agent-sandbox-system", "--create-namespace", "--skip-crds",
		"--set", "namespace.create=false",
		"--set", "image.tag="+AgentSandboxVersion,
		"--set", "controller.extensions=true",
		"--wait", "--timeout", "3m")
}

func (e *env) smokeClaim(ctx context.Context) error {
	e.logf("K1 smoke: SandboxWarmPool with one Ready pod and a bound SandboxClaim")
	pool, err := manifests.ReadFile("manifests/smoke.yaml")
	if err != nil {
		return err
	}
	if err := e.kubectlIn(ctx, []byte(strings.ReplaceAll(string(pool), "${BUSYBOX_IMAGE}", BusyboxImage)), "apply", "-f", "-"); err != nil {
		return err
	}
	if _, err := e.kubectl(ctx, "wait", "sandboxwarmpool/smoke", "-n", "skua-sandboxes", "--for=jsonpath={.status.readyReplicas}=1", "--timeout=180s"); err != nil {
		return err
	}
	if err := e.applyManifest(ctx, "smoke-claim.yaml"); err != nil {
		return err
	}
	if _, err := e.kubectl(ctx, "wait", "sandboxclaim/smoke", "-n", "skua-sandboxes", "--for=condition=Ready", "--timeout=120s"); err != nil {
		return err
	}
	var claim metav1.PartialObjectMetadata
	if _, err := e.getJSON(ctx, &claim, "sandboxclaim", "smoke", "-n", "skua-sandboxes"); err != nil {
		return err
	}
	e.logf("smoke claim bound to sandbox %s", claim.Annotations["agents.x-k8s.io/sandbox-name"])
	_, err = e.kubectl(ctx, "delete", "sandboxclaim/smoke", "sandboxwarmpool/smoke", "sandboxtemplate/smoke", "-n", "skua-sandboxes", "--wait=true")
	return err
}
