// Command kindenv manages the kind test environment.
//
//	kindenv up [flags]
//	kindenv down [flags]
//	kindenv load [flags] IMAGE...
//	kindenv logs [flags] DIR
//	kindenv footprint start|stop [flags]
//
// Flags default to SKUA_KIND_NAME, SKUA_KUBECONFIG, SKUA_GVISOR, SKUA_GVISOR_PLATFORM,
// SKUA_KIND_NODE_IMAGE and SKUA_KIND_CACHE. Run a command with -h for its flags.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Devcon4/skua/test/kindenv"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kindenv:", err)
		os.Exit(1)
	}
}

const usage = "usage: kindenv up|down|load IMAGE...|logs DIR|footprint start|stop [flags]"

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func cacheDir() string {
	if dir := os.Getenv("SKUA_KIND_CACHE"); dir != "" {
		return dir
	}
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "skua-kind")
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	if cmd == "footprint" {
		if len(args) == 0 {
			return errors.New(usage)
		}
		return footprint(ctx, args[0], args[1:])
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	cfg := kindenv.Config{Out: os.Stdout}
	fs.StringVar(&cfg.Name, "name", envOr("SKUA_KIND_NAME", "skua-dev"), "kind cluster name (SKUA_KIND_NAME)")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", os.Getenv("SKUA_KUBECONFIG"), "kubeconfig path; default /tmp/<name>.kubeconfig (SKUA_KUBECONFIG)")
	fs.StringVar(&cfg.GVisor, "gvisor", envOr("SKUA_GVISOR", "auto"), "auto, runsc or runc: what RuntimeClass gvisor maps to (SKUA_GVISOR)")
	fs.StringVar(&cfg.GVisorPlatform, "gvisor-platform", envOr("SKUA_GVISOR_PLATFORM", "auto"), "auto, kvm or systrap: the runsc platform (SKUA_GVISOR_PLATFORM)")
	fs.StringVar(&cfg.NodeImage, "node-image", os.Getenv("SKUA_KIND_NODE_IMAGE"), "kind node image; default kind's (SKUA_KIND_NODE_IMAGE)")
	fs.StringVar(&cfg.CacheDir, "cache", cacheDir(), "download cache (SKUA_KIND_CACHE)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Kubeconfig == "" {
		cfg.Kubeconfig = "/tmp/" + cfg.Name + ".kubeconfig"
	}
	switch cmd {
	case "up":
		return kindenv.Up(ctx, cfg)
	case "down":
		return kindenv.Down(cfg)
	case "load":
		if fs.NArg() == 0 {
			return errors.New("load needs at least one image")
		}
		return kindenv.Load(cfg.Name, fs.Args()...)
	case "logs":
		if fs.NArg() != 1 {
			return errors.New("logs needs one output directory")
		}
		return kindenv.Logs(cfg.Name, fs.Arg(0))
	default:
		return errors.New(usage)
	}
}

func footprint(ctx context.Context, sub string, args []string) error {
	fs := flag.NewFlagSet("footprint "+sub, flag.ContinueOnError)
	name := fs.String("name", envOr("SKUA_KIND_NAME", "skua-dev"), "kind cluster name (SKUA_KIND_NAME)")
	dir := fs.String("dir", filepath.Join(envOr("RUNNER_TEMP", "/tmp"), "skua-kind-footprint"), "samples directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f := kindenv.Footprint{Name: *name, Dir: *dir}
	switch sub {
	case "start":
		self, err := os.Executable()
		if err != nil {
			return err
		}
		if err := f.Start(exec.Command(self, "footprint", "sample", "-name", f.Name, "-dir", f.Dir)); err != nil {
			return err
		}
		fmt.Printf("sampling docker stats of %s-* node containers every 2s into %s\n", f.Name, f.Samples())
		return nil
	case "sample":
		return f.Sample(ctx)
	case "stop":
		var summary io.Writer = os.Stdout
		if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			defer func() { _ = file.Close() }()
			summary = file
		}
		return f.Stop(summary)
	default:
		return errors.New(usage)
	}
}
