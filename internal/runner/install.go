package runner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"

	"github.com/Devcon4/skua/internal/contract/sandbox"
)

const binaryName = "skua"

// Links maps each symlink Install creates beside the skua binary to the skua subcommand that
// argv[0] dispatch runs for it.
var Links = map[string]string{
	sandbox.RunnerLink:    "runner",
	"git-credential-skua": "git-credential",
	"gh":                  "gh",
}

// Install copies the running executable to dir/skua with mode 0755 and points each of
// Links at it. Repeating it is safe.
func Install(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if err := copySelf(filepath.Join(dir, binaryName)); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	for name := range Links {
		if err := linkTo(binaryName, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("install: %w", err)
		}
	}
	return nil
}

func copySelf(dst string) error {
	in, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	pf, err := renameio.NewPendingFile(dst, renameio.WithStaticPermissions(0o755), renameio.WithTempDir(filepath.Dir(dst)))
	if err != nil {
		return err
	}
	defer func() { _ = pf.Cleanup() }()
	if _, err := io.Copy(pf, in); err != nil {
		return err
	}
	return pf.CloseAtomicallyReplace()
}

func linkTo(target, link string) error {
	if got, err := os.Readlink(link); err == nil && got == target {
		return nil
	}
	tmp := link + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
