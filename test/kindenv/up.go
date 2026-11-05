package kindenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kindcluster "sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/yaml"
)

// Up creates the cluster, or updates it in place when it exists, and installs what an installer
// would provide: gVisor and RuntimeClass gvisor, agent-sandbox, Dex, LiteLLM with the operator
// key, the external egress container and the fake GitHub App Secret. It writes EnvFile and
// StateFile, and ends with a SandboxClaim smoke test.
func Up(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	for _, tool := range []string{"docker", "kubectl", "helm", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s not found on PATH", tool)
		}
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return err
	}
	e, err := newEnv(cfg)
	if err != nil {
		return err
	}
	sampler := e.samplePeaks(ctx)
	defer sampler.stop()

	if err := e.ensureCluster(); err != nil {
		return err
	}
	platforms, err := e.installGVisor(ctx)
	if err != nil {
		return err
	}
	if err := e.ensureRuntimeClass(ctx); err != nil {
		return err
	}
	if err := e.installAgentSandbox(ctx); err != nil {
		return err
	}
	if err := e.applyPrereqs(ctx); err != nil {
		return err
	}
	if err := e.ensureGitHubApp(ctx); err != nil {
		return err
	}
	if err := e.installDex(ctx); err != nil {
		return err
	}
	if err := e.installLiteLLM(ctx); err != nil {
		return err
	}
	if err := e.ensureOperatorKey(ctx); err != nil {
		return err
	}
	externalIP, err := e.startExternal(ctx)
	if err != nil {
		return err
	}
	if err := e.writeEnvFile(ctx, externalIP); err != nil {
		return err
	}
	if err := e.smokeClaim(ctx); err != nil {
		return err
	}

	peaks := sampler.stop()
	var rc nodev1.RuntimeClass
	if _, err := e.getJSON(ctx, &rc, "runtimeclass", "gvisor"); err != nil {
		return err
	}
	state := State{
		Name:                cfg.Name,
		Kubeconfig:          cfg.Kubeconfig,
		EnvFile:             EnvFile(cfg.Name),
		WallSeconds:         int(time.Since(e.start).Seconds()),
		RuntimeClassHandler: rc.Handler,
		RunscPlatforms:      platforms,
		PeakNodeMemoryMiB:   peaks,
	}
	e.logf("wall time %ds; peak node memory (docker stats): %s", state.WallSeconds, formatPeaks(peaks))
	if err := writeState(state); err != nil {
		return err
	}
	e.logf("ready: kubeconfig %s, env %s, state %s, RuntimeClass gvisor -> %s", cfg.Kubeconfig, state.EnvFile, StateFile(cfg.Name), rc.Handler)
	return nil
}

func (e *env) ensureCluster() error {
	ok, err := e.exists()
	if err != nil {
		return err
	}
	if ok {
		e.logf("cluster %s exists", e.cfg.Name)
		return e.provider.ExportKubeConfig(e.cfg.Name, e.cfg.Kubeconfig, false)
	}
	e.logf("creating kind cluster %s", e.cfg.Name)
	raw, err := manifests.ReadFile("manifests/kind-config.yaml")
	if err != nil {
		return err
	}
	opts := []kindcluster.CreateOption{
		kindcluster.CreateWithRawConfig(raw),
		kindcluster.CreateWithKubeconfigPath(e.cfg.Kubeconfig),
		kindcluster.CreateWithWaitForReady(120 * time.Second),
		kindcluster.CreateWithDisplayUsage(false),
		kindcluster.CreateWithDisplaySalutation(false),
	}
	if e.cfg.NodeImage != "" {
		opts = append(opts, kindcluster.CreateWithNodeImage(e.cfg.NodeImage))
	}
	if err := e.provider.Create(e.cfg.Name, opts...); err != nil {
		return fmt.Errorf("create kind cluster %s: %w", e.cfg.Name, err)
	}
	return nil
}

func (e *env) ensureNamespace(ctx context.Context, name string, labels map[string]string) error {
	ok, err := e.has(ctx, "namespace", name)
	if err != nil || ok {
		return err
	}
	return e.createObject(ctx, &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	})
}

func (e *env) applyPrereqs(ctx context.Context) error {
	e.logf("applying installer prerequisites")
	if err := e.applyManifest(ctx, "prereqs.yaml"); err != nil {
		return err
	}
	for _, ns := range []string{"skua", "skua-system"} {
		if err := e.ensureNamespace(ctx, ns, nil); err != nil {
			return err
		}
	}
	return e.ensureNamespace(ctx, "skua-sandboxes", map[string]string{"pod-security.kubernetes.io/enforce": "restricted"})
}

func secret(namespace, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
}

func (e *env) secretValue(ctx context.Context, namespace, name, key string) ([]byte, error) {
	var s corev1.Secret
	found, err := e.getJSON(ctx, &s, "secret", name, "-n", namespace)
	if err != nil {
		return nil, err
	}
	v, ok := s.Data[key]
	if !found || !ok {
		return nil, fmt.Errorf("secret %s/%s has no key %s", namespace, name, key)
	}
	return v, nil
}

func (e *env) startExternal(ctx context.Context) (string, error) {
	e.logf("starting external egress target %s on the kind network", ExternalContainer)
	if running, _ := docker(ctx, "inspect", "-f", "{{.State.Running}}", ExternalContainer); running != "true" {
		_, _ = docker(ctx, "rm", "-f", ExternalContainer)
		if _, err := docker(ctx, "run", "-d", "--quiet", "--name", ExternalContainer, "--network", "kind",
			"--restart", "unless-stopped", BusyboxImage,
			"sh", "-c", "mkdir -p /www && echo external >/www/index.html && exec httpd -f -p 8080 -h /www"); err != nil {
			return "", err
		}
	}
	return docker(ctx, "inspect", "-f", `{{(index .NetworkSettings.Networks "kind").IPAddress}}`, ExternalContainer)
}

func (e *env) writeEnvFile(ctx context.Context, externalIP string) error {
	var kubeadm corev1.ConfigMap
	if _, err := e.getJSON(ctx, &kubeadm, "configmap", "kubeadm-config", "-n", "kube-system"); err != nil {
		return err
	}
	var cluster struct {
		Networking struct {
			PodSubnet     string `json:"podSubnet"`
			ServiceSubnet string `json:"serviceSubnet"`
		} `json:"networking"`
	}
	if err := yaml.Unmarshal([]byte(kubeadm.Data["ClusterConfiguration"]), &cluster); err != nil {
		return fmt.Errorf("decode kubeadm ClusterConfiguration: %w", err)
	}
	var node corev1.Node
	if _, err := e.getJSON(ctx, &node, "node", e.cfg.Name+"-control-plane"); err != nil {
		return err
	}
	nodeIP := ""
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			nodeIP = a.Address
			break
		}
	}
	pod, svc := cluster.Networking.PodSubnet, cluster.Networking.ServiceSubnet
	if pod == "" || svc == "" || nodeIP == "" || externalIP == "" {
		return errors.New("could not read pod CIDR, service CIDR, node IP or external IP")
	}
	path := EnvFile(e.cfg.Name)
	content := fmt.Sprintf("SKUA_POD_CIDR=%s\nSKUA_SERVICE_CIDR=%s\nSKUA_NODE_CIDR=%s/32\nSKUA_EXTERNAL_URL=http://%s:8080\n", pod, svc, nodeIP, externalIP)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	e.logf("wrote %s", path)
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
