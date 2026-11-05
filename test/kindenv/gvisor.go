package kindenv

import (
	"archive/tar"
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
)

func (e *env) installGVisor(ctx context.Context) (map[string]string, error) {
	e.logf("installing gVisor %s into nodes", GVisorVersion)
	dir := filepath.Join(e.cfg.CacheDir, "gvisor-"+GVisorVersion)
	if !gvisorReleaseCached(dir) {
		if err := downloadGVisor(ctx, dir); err != nil {
			return nil, err
		}
	}
	runscSum, err := fileSHA256(filepath.Join(dir, "runsc"))
	if err != nil {
		return nil, err
	}
	nodeList, err := e.provider.ListNodes(e.cfg.Name)
	if err != nil {
		return nil, err
	}
	platforms := map[string]string{}
	for _, node := range nodeList {
		platform, err := e.installGVisorOnNode(ctx, node, dir, runscSum)
		if err != nil {
			return nil, err
		}
		platforms[node.String()] = platform
		e.logf("node %s: runsc platform %s", node, platform)
	}
	return platforms, nil
}

func gvisorReleaseCached(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "runsc"))
	return err == nil && info.Mode()&0o111 != 0
}

var gvisorBinaries = map[string]string{
	"runsc":                    "/usr/local/bin/runsc",
	"containerd-shim-runsc-v1": "/usr/local/bin/containerd-shim-runsc-v1",
	"gvisor-bin":               "/usr/local/bin/",
}

func (e *env) installGVisorOnNode(ctx context.Context, node nodes.Node, dir, runscSum string) (string, error) {
	if !nodeHasRunsc(ctx, node, runscSum) {
		if err := copyGVisorBinaries(ctx, node, dir); err != nil {
			return "", err
		}
	}
	platform := e.runscPlatform(ctx, node)
	if err := nodeutils.WriteFile(node, "/etc/containerd/runsc.toml", fmt.Sprintf("[runsc_config]\n  platform = %q\n", platform)); err != nil {
		return "", fmt.Errorf("write runsc.toml on %s: %w", node, err)
	}
	return platform, nil
}

func nodeHasRunsc(ctx context.Context, node nodes.Node, runscSum string) bool {
	var out bytes.Buffer
	_ = node.CommandContext(ctx, "sh", "-c", "sha256sum /usr/local/bin/runsc 2>/dev/null").SetStdout(&out).Run()
	fields := strings.Fields(out.String())
	return len(fields) > 0 && fields[0] == runscSum
}

func copyGVisorBinaries(ctx context.Context, node nodes.Node, dir string) error {
	for src, dst := range gvisorBinaries {
		if _, err := docker(ctx, "cp", filepath.Join(dir, src), node.String()+":"+dst); err != nil {
			return err
		}
	}
	return nil
}

func (e *env) runscPlatform(ctx context.Context, node nodes.Node) string {
	if e.cfg.GVisorPlatform != "auto" {
		return e.cfg.GVisorPlatform
	}
	if node.CommandContext(ctx, "test", "-c", "/dev/kvm").Run() == nil {
		return "kvm"
	}
	return "systrap"
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func downloadGVisor(ctx context.Context, dir string) error {
	url := "https://storage.googleapis.com/gvisor/releases/release/" + GVisorVersion + "/x86_64/gvisor.tar.bz2"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	archive, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	sum := sha512.Sum512(archive)
	if hex.EncodeToString(sum[:]) != GVisorSHA512 {
		return fmt.Errorf("gvisor.tar.bz2 %s does not match the pinned SHA-512", GVisorVersion)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "gvisor-download-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := extractTar(tar.NewReader(bzip2.NewReader(bytes.NewReader(archive))), tmp); err != nil {
		return fmt.Errorf("extract gvisor.tar.bz2: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

func extractTar(r *tar.Reader, dest string) error {
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extractTarEntry(r, hdr, dest); err != nil {
			return err
		}
	}
}

func extractTarEntry(r *tar.Reader, hdr *tar.Header, dest string) error {
	if !filepath.IsLocal(hdr.Name) {
		return fmt.Errorf("entry %q leaves the archive", hdr.Name)
	}
	path := filepath.Join(dest, hdr.Name)
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(path, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return writeTarFile(r, path, hdr.FileInfo().Mode().Perm())
	}
	return fmt.Errorf("entry %q has unsupported type %c", hdr.Name, hdr.Typeflag)
}

func writeTarFile(r *tar.Reader, path string, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (e *env) applyRuntimeClass(ctx context.Context, name, handler string) error {
	return e.applyObject(ctx, &nodev1.RuntimeClass{
		TypeMeta:   metav1.TypeMeta{APIVersion: "node.k8s.io/v1", Kind: "RuntimeClass"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Handler:    handler,
	})
}

func (e *env) mapGVisor(ctx context.Context, handler string) error {
	var current nodev1.RuntimeClass
	found, err := e.getJSON(ctx, &current, "runtimeclass", "gvisor")
	if err != nil {
		return err
	}
	if found && current.Handler != handler {
		if _, err := e.kubectl(ctx, "delete", "runtimeclass", "gvisor"); err != nil {
			return err
		}
	}
	return e.applyRuntimeClass(ctx, "gvisor", handler)
}

func (e *env) ensureRuntimeClass(ctx context.Context) error {
	if e.cfg.GVisor == "runc" {
		if err := e.mapGVisor(ctx, "runc"); err != nil {
			return err
		}
		e.logf("RuntimeClass gvisor -> runc (gvisor mode runc)")
		return nil
	}
	e.logf("gVisor smoke pod")
	if err := e.deleteSmokePod(ctx); err != nil {
		return err
	}
	// A new handler on an existing RuntimeClass name can reach the kubelet late; this class never changes handler.
	if err := e.applyRuntimeClass(ctx, "gvisor-smoke", "runsc"); err != nil {
		return err
	}
	if _, err := e.kubectl(ctx, "run", "gvisor-smoke", "-n", "default", "--image="+BusyboxImage, "--restart=Never",
		`--overrides={"spec":{"runtimeClassName":"gvisor-smoke"}}`, "--", "dmesg"); err != nil {
		return err
	}
	passed := e.smokePodRanUnderGVisor(ctx)
	switch {
	case passed:
		if err := e.mapGVisorAfterSmoke(ctx, "runsc", "runsc"); err != nil {
			return err
		}
	case e.cfg.GVisor == "auto":
		_, _ = fmt.Fprintln(e.cfg.Out, e.smokeEvent(ctx))
		if err := e.mapGVisorAfterSmoke(ctx, "runc", "runc (fallback)"); err != nil {
			return err
		}
	default:
		return e.failSmoke(ctx)
	}
	_, err := e.kubectl(ctx, "delete", "runtimeclass", "gvisor-smoke", "--ignore-not-found")
	return err
}

func (e *env) deleteSmokePod(ctx context.Context) error {
	if _, err := e.kubectl(ctx, "delete", "pod", "gvisor-smoke", "-n", "default", "--ignore-not-found", "--grace-period=0", "--force"); err != nil {
		return fmt.Errorf("could not delete pod default/gvisor-smoke: %w", err)
	}
	return nil
}

func (e *env) smokePodRanUnderGVisor(ctx context.Context) bool {
	if _, err := e.kubectl(ctx, "wait", "pod/gvisor-smoke", "-n", "default", "--for=jsonpath={.status.phase}=Succeeded", "--timeout=90s"); err != nil {
		return false
	}
	logs, err := e.kubectl(ctx, "logs", "gvisor-smoke", "-n", "default")
	return err == nil && strings.Contains(string(logs), "gVisor")
}

func (e *env) mapGVisorAfterSmoke(ctx context.Context, handler, label string) error {
	if err := e.deleteSmokePod(ctx); err != nil {
		return err
	}
	if err := e.mapGVisor(ctx, handler); err != nil {
		return err
	}
	e.logf("RuntimeClass gvisor -> %s", label)
	return nil
}

func (e *env) failSmoke(ctx context.Context) error {
	event := e.smokeEvent(ctx)
	if err := e.deleteSmokePod(ctx); err != nil {
		return err
	}
	if _, err := e.kubectl(ctx, "delete", "runtimeclass", "gvisor-smoke", "--ignore-not-found"); err != nil {
		return err
	}
	return fmt.Errorf("runsc smoke pod failed and gvisor mode is runsc: %s", event)
}

func (e *env) smokeEvent(ctx context.Context) string {
	out, _ := e.kubectl(ctx, "get", "events", "-n", "default", "--field-selector", "involvedObject.name=gvisor-smoke",
		"-o", "custom-columns=MSG:.message", "--no-headers")
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[len(lines)-1]
}
