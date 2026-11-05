package omprpctest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const fillPath = "/work/fill"

func (r *run) orphanThenCrash() error {
	st := r.state()
	if st.Crashed {
		n, err := countOrphans()
		if err != nil {
			return err
		}
		return r.text(fmt.Sprintf("orphans: %d", n))
	}
	r.startTool("bash", map[string]any{"command": "setsid sleep 777"})
	cmd := exec.Command("sleep", "777")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	st.Crashed = true
	if err := r.save(); err != nil {
		return err
	}
	r.a.exit <- 1
	<-r.ctx.Done()
	return nil
}

func countOrphans() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(args) == 2 && filepath.Base(args[0]) == "sleep" && args[1] == "777" {
			n++
		}
	}
	return n, nil
}

func (r *run) termRunner() error {
	r.startTool("bash", map[string]any{"command": "kill -TERM $PPID"})
	if err := syscall.Kill(os.Getppid(), syscall.SIGTERM); err != nil {
		return fmt.Errorf("term-runner: %w", err)
	}
	<-r.ctx.Done()
	return nil
}

func (r *run) fill(args []string) error {
	n, err := intArg(args, 0)
	if err != nil {
		return err
	}
	var hold time.Duration
	if len(args) > 1 {
		if hold, err = durArg(args, 1); err != nil {
			return err
		}
	}
	err = r.bash(fmt.Sprintf("fill %s %d", fillPath, n), func(*toolCall) (string, error) {
		f, err := os.Create(fillPath)
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		buf := make([]byte, 1<<20)
		for left := n; left > 0 && r.ctx.Err() == nil; {
			w, err := f.Write(buf[:min(len(buf), left)])
			if err != nil {
				return "", err
			}
			left -= w
		}
		if err := f.Sync(); err != nil {
			return "", err
		}
		r.wait(hold)
		return "", nil
	})
	if err != nil || r.ctx.Err() != nil {
		return err
	}
	return r.text(fmt.Sprintf("fill: %d bytes", n))
}

func scriptedError(args []string) error {
	status, err := intArg(args, 0)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return errors.New("error needs <httpStatus> <type>")
	}
	return &promptError{
		HTTPStatus: status,
		Message:    fmt.Sprintf("%d scripted error (type=%s param=None code=%d)", status, args[1], status),
	}
}
