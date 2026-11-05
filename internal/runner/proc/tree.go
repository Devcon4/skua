package proc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const killPollInterval = 20 * time.Millisecond

// Start starts cmd as the leader of a new process group; its status is collected
// with (*Reaper).Wait, never cmd.Wait.
func Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	children.Lock()
	defer children.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	children.started[cmd.Process.Pid] = true
	return nil
}

// KillTree sends SIGTERM to every other process in the container, waits up to grace,
// then sends SIGKILL until none remain. As PID 1 it targets every process but itself;
// otherwise only its descendants. It returns ctx's error if processes outlive ctx.
func KillTree(ctx context.Context, grace time.Duration) error {
	self := os.Getpid()
	termed := map[int]bool{}
	sig := syscall.SIGTERM
	escalate := time.NewTimer(grace)
	defer escalate.Stop()
	tick := time.NewTicker(killPollInterval)
	defer tick.Stop()
	for {
		pids, err := treeTargets(self)
		if err != nil {
			return fmt.Errorf("kill tree: %w", err)
		}
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			if sig == syscall.SIGTERM && termed[pid] {
				continue
			}
			termed[pid] = true
			_ = syscall.Kill(pid, sig)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kill tree: %d processes alive: %w", len(pids), ctx.Err())
		case <-escalate.C:
			sig = syscall.SIGKILL
		case <-tick.C:
		}
	}
}

type procStat struct {
	pid, ppid, pgrp, session int
	state                    byte
}

func (s procStat) dead() bool {
	return s.state == 'Z' || s.state == 'X' || s.state == 'x'
}

func treeTargets(self int) ([]int, error) {
	stats, err := allStats()
	if err != nil {
		return nil, err
	}
	var targets []int
	if self == 1 {
		for _, s := range stats {
			if s.pid != self && !s.dead() {
				targets = append(targets, s.pid)
			}
		}
		return targets, nil
	}
	kids := map[int][]procStat{}
	for _, s := range stats {
		kids[s.ppid] = append(kids[s.ppid], s)
	}
	queue := []int{self}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, s := range kids[parent] {
			queue = append(queue, s.pid)
			if !s.dead() {
				targets = append(targets, s.pid)
			}
		}
	}
	return targets, nil
}

func allStats() ([]procStat, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	stats := make([]procStat, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		s, err := readStat(pid)
		if err != nil {
			continue
		}
		stats = append(stats, s)
	}
	return stats, nil
}

func readStat(pid int) (procStat, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return procStat{}, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	fields := bytes.Fields(raw[end+1:])
	if len(fields) < 4 || len(fields[0]) != 1 {
		return procStat{}, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	s := procStat{pid: pid, state: fields[0][0]}
	for i, dst := range []*int{&s.ppid, &s.pgrp, &s.session} {
		if *dst, err = strconv.Atoi(string(fields[i+1])); err != nil {
			return procStat{}, fmt.Errorf("parse /proc/%d/stat: %w", pid, err)
		}
	}
	return s, nil
}
