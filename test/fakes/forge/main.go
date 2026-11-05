package main

import (
	"context"
	"crypto/rsa"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	publicKeyEnv  = "FORGE_APP_PUBLIC_KEY"
	gitUser       = "x-access-token"
	branchPrefix  = "skua/"
	jwtClockSkew  = 60 * time.Second
	jwtMaxFuture  = 10 * time.Minute
	maxBodyBytes  = 1 << 20
	shaPrefixHex  = 12
	defaultBranch = "main"
)

var seedRepos = []string{"e2e/sample", "e2e/other"}

type forge struct {
	root    string
	key     *rsa.PublicKey
	ttl     time.Duration
	now     func() time.Time
	repos   map[string]bool
	backend http.Handler

	mu      sync.Mutex
	tokens  []*tokenRecord
	byValue map[string]*tokenRecord
	pulls   map[string][]pull
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("fakeforge: %v", err)
	}
}

func run() error {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	ttl := flag.Duration("token-ttl", 20*time.Minute, "lifetime of minted installation tokens")
	root := flag.String("root", "", "directory for the bare repositories; empty creates a temporary directory")
	flag.Parse()

	key, err := parsePublicKey(os.Getenv(publicKeyEnv))
	if err != nil {
		return err
	}
	if *root == "" {
		if *root, err = os.MkdirTemp("", "fakeforge-"); err != nil {
			return err
		}
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return fmt.Errorf("git --exec-path: %w", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		return err
	}

	f := &forge{
		root:  *root,
		key:   key,
		ttl:   *ttl,
		now:   time.Now,
		repos: map[string]bool{},
		backend: &cgi.Handler{
			Path:   backend,
			Env:    []string{"GIT_PROJECT_ROOT=" + *root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + gitUser, "HOME=" + *root, "GIT_CONFIG_NOSYSTEM=1"},
			Stderr: os.Stderr,
		},
		byValue: map[string]*tokenRecord{},
		pulls:   map[string][]pull{},
	}
	for _, repo := range seedRepos {
		if err := f.seed(repo); err != nil {
			return fmt.Errorf("seed %s: %w", repo, err)
		}
		f.repos[repo] = true
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	srv := &http.Server{Addr: *listen, Handler: f.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("fakeforge: serving %v from %s on %s, token TTL %s", seedRepos, *root, *listen, *ttl)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
