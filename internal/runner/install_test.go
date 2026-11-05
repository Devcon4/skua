package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	for range 2 {
		if err := Install(dir); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	info, err := os.Lstat(filepath.Join(dir, "skua"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		t.Fatalf("skua mode %v, want regular 0755", info.Mode())
	}
	self, err := os.Stat("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != self.Size() {
		t.Fatalf("skua size %d, want %d", info.Size(), self.Size())
	}
	for _, name := range []string{"skua-runner", "git-credential-skua", "gh"} {
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil || target != "skua" {
			t.Fatalf("%s -> %q (%v), want skua", name, target, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("dir has %d entries, want 4: %v", len(entries), entries)
	}
	if out, err := exec.Command(filepath.Join(dir, "gh"), "-test.run=^$").CombinedOutput(); err != nil {
		t.Fatalf("run installed binary through gh link: %v\n%s", err, out)
	}
}
