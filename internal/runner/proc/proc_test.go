package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const childModeEnv = "SKUA_PROC_TEST_CHILD"

func TestMain(m *testing.M) {
	switch mode := os.Getenv(childModeEnv); mode {
	case "":
		// Race-built children otherwise sleep 1 s at exit.
		_ = os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
		os.Exit(m.Run())
	case "killtree":
		os.Exit(childExit(killTreeAsPID1()))
	case "reaper":
		os.Exit(childExit(reaperAsPID1()))
	default:
		fmt.Fprintf(os.Stderr, "unknown %s %q\n", childModeEnv, mode)
		os.Exit(3)
	}
}

func childExit(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestStartOwnGroupAndKillTreeDescendants(t *testing.T) {
	r := StartReaper()
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	cmd := exec.Command("sh", "-c", `sleep 600 & echo $! > "$0.tmp" && mv "$0.tmp" "$0"; exec sleep 600`, pidFile)
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	grandchild, err := waitPidFile(pidFile, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	st, err := readStat(pid)
	if err != nil {
		t.Fatal(err)
	}
	if st.pgrp != pid {
		t.Fatalf("child pgid %d, want %d", st.pgrp, pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := KillTree(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	ws := r.Wait(pid)
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("child status %v, want SIGTERM", ws)
	}
	deadline := time.Now().Add(time.Second)
	for {
		st, err := readStat(grandchild)
		if err != nil || st.dead() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d alive in state %c", grandchild, st.state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKillTreeEscapedSessionAsPID1(t *testing.T) {
	runAsPID1(t, "killtree")
}

func TestReaperOrphanAsPID1(t *testing.T) {
	runAsPID1(t, "reaper")
}

func runAsPID1(t *testing.T, mode string) {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skipf("unshare not found, PID namespace tests skipped: %v", err)
	}
	args := []string{"--pid", "--fork", "--mount-proc"}
	if os.Geteuid() != 0 {
		args = append([]string{"--user", "--map-root-user"}, args...)
	}
	if out, err := exec.Command(unshare, append(args, "true")...).CombinedOutput(); err != nil {
		t.Skipf("unshare %s unavailable, PID namespace tests skipped: %v: %s", strings.Join(args, " "), err, out)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(unshare, append(args, self)...)
	cmd.Env = append(os.Environ(), childModeEnv+"="+mode)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s as PID 1: %v\n%s", mode, err, out)
	}
}

func killTreeAsPID1() error {
	if os.Getpid() != 1 {
		return fmt.Errorf("pid %d, want 1", os.Getpid())
	}
	r := StartReaper()
	dir, err := os.MkdirTemp("", "skua-proc-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	pidFile := filepath.Join(dir, "escaped")
	agent := exec.Command("sh", "-c",
		`setsid sh -c 'trap "" TERM; echo $$ > "$0.tmp" && mv "$0.tmp" "$0"; exec sleep 600' "$0" & exec sleep 600`,
		pidFile)
	if err := Start(agent); err != nil {
		return err
	}
	escaped, err := waitPidFile(pidFile, 5*time.Second)
	if err != nil {
		return err
	}
	st, err := readStat(escaped)
	if err != nil {
		return err
	}
	if st.session != escaped || st.pgrp == agent.Process.Pid {
		return fmt.Errorf("escaped %d has session %d pgid %d; want its own session outside group %d", escaped, st.session, st.pgrp, agent.Process.Pid)
	}
	const grace = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	began := time.Now()
	if err := KillTree(ctx, grace); err != nil {
		return err
	}
	if took := time.Since(began); took < grace {
		return fmt.Errorf("KillTree returned after %v, before the %v grace, though the escaped process ignores SIGTERM", took, grace)
	}
	if ws := r.Wait(agent.Process.Pid); !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		return fmt.Errorf("agent status %v, want SIGTERM", ws)
	}
	return waitOnlyPID1(time.Second)
}

func reaperAsPID1() error {
	if os.Getpid() != 1 {
		return fmt.Errorf("pid %d, want 1", os.Getpid())
	}
	r := StartReaper()
	early := exec.Command("sh", "-c", "exit 7")
	if err := Start(early); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	if ws := r.Wait(early.Process.Pid); ws.ExitStatus() != 7 {
		return fmt.Errorf("early exit status %v, want 7", ws)
	}
	dir, err := os.MkdirTemp("", "skua-proc-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	pidFile := filepath.Join(dir, "orphan")
	const orphanLife = 500 * time.Millisecond
	parent := exec.Command("sh", "-c", `sleep 0.5 & echo $! > "$0.tmp" && mv "$0.tmp" "$0"`, pidFile)
	if err := Start(parent); err != nil {
		return err
	}
	if ws := r.Wait(parent.Process.Pid); ws.ExitStatus() != 0 {
		return fmt.Errorf("parent status %v, want 0", ws)
	}
	orphan, err := waitPidFile(pidFile, time.Second)
	if err != nil {
		return err
	}
	if st, err := readStat(orphan); err == nil && st.ppid != 1 {
		return fmt.Errorf("orphan %d has ppid %d, want 1", orphan, st.ppid)
	}
	deadline := time.Now().Add(orphanLife + time.Second)
	for {
		st, err := readStat(orphan)
		if errors.Is(err, os.ErrNotExist) {
			return waitOnlyPID1(time.Second)
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("orphan %d still in state %c one second after exit", orphan, st.state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitOnlyPID1(within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		stats, err := allStats()
		if err != nil {
			return err
		}
		var others []string
		for _, s := range stats {
			if s.pid != 1 {
				others = append(others, fmt.Sprintf("%d(%c)", s.pid, s.state))
			}
		}
		if len(others) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("processes besides PID 1 remain: %s", strings.Join(others, " "))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitPidFile(path string, within time.Duration) (int, error) {
	deadline := time.Now().Add(within)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			return strconv.Atoi(string(bytes.TrimSpace(raw)))
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("pid file %s: %w", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
