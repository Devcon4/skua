package proc

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Reaper collects child exit statuses. As PID 1 it reaps every child, including
// orphans re-parented to it; otherwise it waits only for the pid passed to Wait.
type Reaper struct {
	all bool
}

var (
	reaperOnce sync.Once
	theReaper  *Reaper

	children = struct {
		sync.Mutex
		started map[int]bool
		waiters map[int]chan syscall.WaitStatus
		exited  map[int]syscall.WaitStatus
	}{
		started: map[int]bool{},
		waiters: map[int]chan syscall.WaitStatus{},
		exited:  map[int]syscall.WaitStatus{},
	}
)

// StartReaper returns the process-wide Reaper, starting the SIGCHLD reaping loop
// on the first call when the caller is PID 1.
func StartReaper() *Reaper {
	reaperOnce.Do(func() {
		theReaper = &Reaper{all: os.Getpid() == 1}
		if !theReaper.all {
			return
		}
		ch := make(chan os.Signal, 64)
		signal.Notify(ch, syscall.SIGCHLD)
		go func() {
			reapAll()
			for range ch {
				reapAll()
			}
		}()
	})
	return theReaper
}

func reapAll() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			continue
		}
		if pid <= 0 || err != nil {
			return
		}
		children.Lock()
		if children.started[pid] {
			delete(children.started, pid)
			if w, ok := children.waiters[pid]; ok {
				delete(children.waiters, pid)
				w <- ws
			} else {
				children.exited[pid] = ws
			}
		}
		children.Unlock()
	}
}

// Wait blocks until the child pid, started with Start, exits and returns its status.
func (r *Reaper) Wait(pid int) syscall.WaitStatus {
	if !r.all {
		var ws syscall.WaitStatus
		for {
			if _, err := syscall.Wait4(pid, &ws, 0, nil); err != syscall.EINTR {
				break
			}
		}
		children.Lock()
		delete(children.started, pid)
		children.Unlock()
		return ws
	}
	children.Lock()
	if ws, ok := children.exited[pid]; ok {
		delete(children.exited, pid)
		children.Unlock()
		return ws
	}
	ch := make(chan syscall.WaitStatus, 1)
	children.waiters[pid] = ch
	children.Unlock()
	return <-ch
}
