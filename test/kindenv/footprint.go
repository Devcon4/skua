package kindenv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Footprint records the memory of a cluster's node containers between Start and Stop, and
// summarizes it with the State that Up wrote.
type Footprint struct {
	// Name is the kind cluster name; node containers are named <Name>-*.
	Name string
	// Dir holds the samples and the sampler's pid file.
	Dir string
}

// Samples is the file Sample appends to.
func (f Footprint) Samples() string { return filepath.Join(f.Dir, "samples") }
func (f Footprint) pidFile() string { return filepath.Join(f.Dir, "sampler.pid") }

// Start launches sampler, a command that runs Sample, detached from the caller. It fails when a
// sampler from an earlier Start is still running.
func (f Footprint) Start(sampler *exec.Cmd) error {
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return err
	}
	if pid, err := f.pid(); err == nil && syscall.Kill(pid, 0) == nil {
		return fmt.Errorf("sampler already running (pid %d)", pid)
	}
	if err := os.WriteFile(f.Samples(), nil, 0o644); err != nil {
		return err
	}
	sampler.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := sampler.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(f.pidFile(), []byte(strconv.Itoa(sampler.Process.Pid)), 0o644); err != nil {
		return err
	}
	return sampler.Process.Release()
}

func (f Footprint) pid() (int, error) {
	raw, err := os.ReadFile(f.pidFile())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

// Sample appends one line per node container every 2 seconds until ctx ends.
func (f Footprint) Sample(ctx context.Context) error {
	out, err := os.OpenFile(f.Samples(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	for {
		names, err := docker(ctx, "ps", "--filter", "name=^"+f.Name+"-", "--format", "{{.Names}}")
		if err == nil && names != "" {
			if usage, err := dockerStats(ctx, strings.Fields(names)); err == nil {
				now := time.Now().Unix()
				for n, mib := range usage {
					_, _ = fmt.Fprintf(out, "%d %s %.1f\n", now, n, mib)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// Stop ends the sampler and appends a Markdown summary to summary: runner size, Up's wall time
// and runsc platforms, and peak memory per node and for all nodes together.
func (f Footprint) Stop(summary io.Writer) error {
	if pid, err := f.pid(); err == nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		_ = os.Remove(f.pidFile())
	}
	peaks, all, err := f.peaks()
	if err != nil {
		return err
	}
	state, stateErr := ReadState(f.Name)
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return stateErr
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### kind footprint (%s)\n", f.Name)
	fmt.Fprintf(&b, "- runner nproc: %d\n", runtime.NumCPU())
	fmt.Fprintf(&b, "- runner MemTotal: %s\n", memTotal())
	if stateErr == nil {
		fmt.Fprintf(&b, "- kindenv up wall time: %d s\n", state.WallSeconds)
	} else {
		fmt.Fprintf(&b, "- kindenv up wall time: no state file %s\n", StateFile(f.Name))
	}
	if len(peaks) == 0 {
		b.WriteString("- peak node memory: no samples\n")
	}
	for _, n := range slices.Sorted(maps.Keys(peaks)) {
		fmt.Fprintf(&b, "- peak node memory: %s %.0f MiB\n", n, peaks[n])
	}
	if len(peaks) > 1 {
		fmt.Fprintf(&b, "- peak node memory: all nodes %.0f MiB\n", all)
	}
	if len(state.RunscPlatforms) == 0 {
		fmt.Fprintf(&b, "- runsc platform: none recorded in %s\n", StateFile(f.Name))
	}
	for _, n := range slices.Sorted(maps.Keys(state.RunscPlatforms)) {
		fmt.Fprintf(&b, "- runsc platform: `node %s: runsc platform %s`\n", n, state.RunscPlatforms[n])
	}
	_, err = io.WriteString(summary, b.String())
	return err
}

func (f Footprint) peaks() (map[string]float64, float64, error) {
	file, err := os.Open(f.Samples())
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	peaks := map[string]float64{}
	totals := map[string]float64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			continue
		}
		mib, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			continue
		}
		peaks[fields[1]] = max(peaks[fields[1]], mib)
		totals[fields[0]] += mib
	}
	all := 0.0
	for _, t := range totals {
		all = max(all, t)
	}
	return peaks, all, scanner.Err()
}

func memTotal() string {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "unknown"
	}
	for line := range strings.Lines(string(raw)) {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			return strings.Join(strings.Fields(rest), " ")
		}
	}
	return "unknown"
}
