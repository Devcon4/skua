//go:build install

package install

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
)

const (
	sseHost    = "sse.127.0.0.1.nip.io"
	sseAddress = "127.0.0.1:8443"
)

var durationRe = regexp.MustCompile(`^[1-9][0-9]*$`)

func TestIngressStream(t *testing.T) {
	const name = "skua-ingress"
	durationArg := envOr("DURATION", "90")
	if !durationRe.MatchString(durationArg) {
		t.Fatalf("DURATION=%s; want a positive number of seconds", durationArg)
	}
	duration, err := strconv.Atoi(durationArg)
	if err != nil {
		t.Fatalf("DURATION=%s; want a positive number of seconds", durationArg)
	}
	requireTools(t, "docker", "kubectl", helmBin())

	t.Logf("creating kind cluster %s", name)
	k := newKube(t, name, os.Getenv("SKUA_KIND_NODE_IMAGE"), &v1alpha4.Cluster{
		TypeMeta: v1alpha4.TypeMeta{Kind: "Cluster", APIVersion: "kind.x-k8s.io/v1alpha4"},
		Nodes: []v1alpha4.Node{{
			Role:              v1alpha4.ControlPlaneRole,
			ExtraPortMappings: []v1alpha4.PortMapping{{ContainerPort: 443, HostPort: 8443, ListenAddress: "127.0.0.1"}},
		}},
	})

	t.Logf("installing ingress-nginx chart %s (controller %s@%s)", ingressChartVersion, ingressImageTag, ingressImageDigest)
	charts := t.TempDir()
	k.helm("pull", "ingress-nginx", "--repo", ingressChartRepository, "--version", ingressChartVersion, "--destination", charts)
	chart := filepath.Join(charts, "ingress-nginx-"+ingressChartVersion+".tgz")
	values := []string{
		"--set", "controller.image.tag=" + ingressImageTag,
		"--set", "controller.image.digest=" + ingressImageDigest,
		"--set", "controller.hostPort.enabled=true",
		"--set", "controller.service.type=ClusterIP",
		"--set", "controller.admissionWebhooks.enabled=false",
	}
	k.pullDeployments(k.helm(append([]string{"template", "ingress-nginx", chart, "--namespace", "ingress-nginx"}, values...)...))
	k.helm(append(append([]string{"upgrade", "--install", "ingress-nginx", chart, "--namespace", "ingress-nginx", "--create-namespace"},
		values...), "--wait", "--timeout", "3m")...)

	t.Logf("making a local CA and a server certificate for %s", sseHost)
	caPEM, certPEM, keyPEM := newServerCertificate(t, sseHost)

	t.Log("applying sse-echo.yaml and Secret sse-echo/sse-tls")
	sse := readTestdata(t, "sse-echo.yaml")
	k.pullDeployments(sse)
	k.apply(sse)
	k.applyObject(&corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "sse-tls", Namespace: "sse-echo"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
	})
	k.must("rollout", "status", "deployment", "sse-echo", "-n", "sse-echo", "--timeout=180s")

	client := sseClient(t, caPEM)
	healthz := "https://" + sseHost + ":8443/healthz"
	for tries := 60; !healthy(t, client, healthz); tries-- {
		if tries == 1 {
			t.Fatalf("FAIL K6: %s did not answer over TLS with the local CA", healthz)
		}
		time.Sleep(2 * time.Second)
	}

	minHeartbeats, minTicks := duration/15-4, duration/60-1
	url := "https://" + sseHost + ":8443/"
	t.Logf("streaming %s for %ds (want the stream open at the deadline, >= %d heartbeats, >= %d ticks)", url, duration, minHeartbeats, minTicks)
	held, elapsed, heartbeats, ticks, streamErr := stream(t, client, url, time.Duration(duration)*time.Second)
	if !held {
		t.Fatalf("FAIL K6: stream closed after %ds of %ds (%v, %d heartbeats, %d ticks)", elapsed, duration, streamErr, heartbeats, ticks)
	}
	if heartbeats < minHeartbeats || ticks < minTicks {
		t.Fatalf("FAIL K6: stream held %ds but carried %d heartbeats (want >= %d) and %d ticks (want >= %d)",
			elapsed, heartbeats, minHeartbeats, ticks, minTicks)
	}
	t.Logf("PASS K6: TLS stream through ingress-nginx %s held %ds with %d heartbeats and %d ticks", ingressChartVersion, elapsed, heartbeats, ticks)
}

func newServerCertificate(t *testing.T, host string) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: "skua-ingress-check-ca"},
		NotBefore:             now,
		NotAfter:              now.AddDate(0, 0, 1),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := sha1.Sum(x509.MarshalPKCS1PublicKey(&key.PublicKey))
	serverDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now,
		NotAfter:              now.AddDate(0, 0, 1),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		SubjectKeyId:          keyID[:],
	}, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	return serial
}

// sseClient trusts only the local CA and dials the kind port mapping, so the check does not depend on nip.io DNS.
func sseClient(t *testing.T, caPEM []byte) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("local CA PEM has no certificate")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, sseAddress)
		},
	}}
}

func healthy(t *testing.T, client *http.Client, url string) bool {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < http.StatusBadRequest
}

func stream(t *testing.T, client *http.Client, url string, hold time.Duration) (held bool, elapsed, heartbeats, ticks int, err error) {
	ctx, cancel := context.WithTimeout(t.Context(), hold)
	defer cancel()
	began := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err == nil {
		lines := bufio.NewScanner(resp.Body)
		for lines.Scan() {
			switch lines.Text() {
			case ": hb":
				heartbeats++
			case "event: tick":
				ticks++
			}
		}
		err = lines.Err()
		if err == nil {
			err = errors.New("HTTP " + resp.Status + " body ended")
		}
		_ = resp.Body.Close()
	}
	return errors.Is(ctx.Err(), context.DeadlineExceeded), int(time.Since(began).Seconds()), heartbeats, ticks, err
}
