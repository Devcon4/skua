package kindenv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const dexHost = "dex.127.0.0.1.nip.io"

func (e *env) installDex(ctx context.Context) error {
	e.logf("installing Dex %s", DexChartVersion)
	if err := e.ensureNamespace(ctx, "dex", nil); err != nil {
		return err
	}
	if err := e.ensureDexTestUsers(ctx); err != nil {
		return err
	}
	values, err := manifestFile("dex-values.yaml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(values) }()
	if err := e.helm(ctx, "upgrade", "--install", "dex", "dex", "--repo", dexChartRepository, "--version", DexChartVersion,
		"--namespace", "dex", "-f", values, "--wait", "--timeout", "3m"); err != nil {
		return err
	}
	if err := e.ensureDexDNSRewrite(ctx); err != nil {
		return err
	}
	if err := retryHTTP(ctx, DexIssuer+"/.well-known/openid-configuration", 30); err != nil {
		return err
	}
	e.logf("Dex issuer %s answers from the host", DexIssuer)
	return nil
}

func (e *env) ensureDexTestUsers(ctx context.Context) error {
	ok, err := e.has(ctx, "secret", "dex-test-users", "-n", "dex")
	if err != nil || ok {
		return err
	}
	adminPassword, devPassword := randomHex(12), randomHex(12)
	adminHash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), 10)
	if err != nil {
		return err
	}
	devHash, err := bcrypt.GenerateFromPassword([]byte(devPassword), 10)
	if err != nil {
		return err
	}
	return e.createObject(ctx, secret("dex", "dex-test-users", map[string][]byte{
		"admin-password": []byte(adminPassword),
		"dev-password":   []byte(devPassword),
		"admin-hash":     adminHash,
		"dev-hash":       devHash,
	}))
}

func (e *env) ensureDexDNSRewrite(ctx context.Context) error {
	var coredns corev1.ConfigMap
	if _, err := e.getJSON(ctx, &coredns, "configmap", "coredns", "-n", "kube-system"); err != nil {
		return err
	}
	corefile := coredns.Data["Corefile"]
	if strings.Contains(corefile, dexHost) {
		return nil
	}
	raw, err := json.Marshal(&corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
		Data:       map[string]string{"Corefile": addDexRewrite(corefile)},
	})
	if err != nil {
		return err
	}
	if err := e.kubectlIn(ctx, raw, "replace", "-f", "-"); err != nil {
		return err
	}
	if _, err := e.kubectl(ctx, "rollout", "restart", "deployment", "coredns", "-n", "kube-system"); err != nil {
		return err
	}
	_, err = e.kubectl(ctx, "rollout", "status", "deployment", "coredns", "-n", "kube-system", "--timeout=90s")
	return err
}

func addDexRewrite(corefile string) string {
	lines := strings.Split(corefile, "\n")
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		out = append(out, line)
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if line[len(indent):] == "ready" {
			out = append(out, indent+"rewrite name "+dexHost+" dex.dex.svc.cluster.local")
		}
	}
	return strings.Join(out, "\n")
}

func manifestFile(name string) (string, error) {
	raw, err := manifests.ReadFile("manifests/" + name)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "kindenv-*-"+name)
	if err != nil {
		return "", err
	}
	_, err = f.Write(raw)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func retryHTTP(ctx context.Context, url string, tries int) error {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for i := 1; ; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
		}
		if i >= tries {
			return fmt.Errorf("%s did not answer", url)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
