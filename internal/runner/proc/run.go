package proc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const outputDrainWait = time.Second

// Run runs cmd with combined output through Start and the reaper, since as PID 1 the reaper
// collects every child and cmd.Wait would race it. Cancelling ctx kills the command's process group.
func Run(ctx context.Context, cmd *exec.Cmd) (string, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	err = Start(cmd)
	_ = pw.Close()
	if err != nil {
		_ = pr.Close()
		return "", err
	}
	var out bytes.Buffer
	read := make(chan struct{})
	go func() {
		defer close(read)
		_, _ = io.Copy(&out, pr)
	}()
	waited := make(chan syscall.WaitStatus, 1)
	pid := cmd.Process.Pid
	go func() { waited <- StartReaper().Wait(pid) }()
	var ws syscall.WaitStatus
	select {
	case ws = <-waited:
	case <-ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		ws = <-waited
	}
	t := time.NewTimer(outputDrainWait)
	select {
	case <-read:
	case <-t.C:
	}
	t.Stop()
	_ = pr.Close()
	<-read
	switch {
	case ctx.Err() != nil:
		return out.String(), ctx.Err()
	case ws.Signaled():
		return out.String(), fmt.Errorf("killed by %s", ws.Signal())
	case ws.ExitStatus() != 0:
		return out.String(), fmt.Errorf("exit status %d", ws.ExitStatus())
	}
	return out.String(), nil
}
