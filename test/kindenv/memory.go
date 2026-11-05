package kindenv

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var mibPerUnit = map[string]float64{
	"B":   1.0 / (1 << 20),
	"KiB": 1.0 / 1024,
	"MiB": 1,
	"GiB": 1024,
	"TiB": 1 << 20,
}

func parseMiB(s string) (float64, bool) {
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	factor, ok := mibPerUnit[s[i:]]
	if err != nil || !ok {
		return 0, false
	}
	return v * factor, true
}

func dockerStats(ctx context.Context, containers []string) (map[string]float64, error) {
	out, err := docker(ctx, append([]string{"stats", "--no-stream", "--format", "{{.Name}} {{.MemUsage}}"}, containers...)...)
	if err != nil {
		return nil, err
	}
	usage := map[string]float64{}
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if mib, ok := parseMiB(fields[1]); ok {
			usage[fields[0]] = mib
		}
	}
	return usage, nil
}

type peakSampler struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	peaks  map[string]float64
}

func (e *env) samplePeaks(ctx context.Context) *peakSampler {
	ctx, cancel := context.WithCancel(ctx)
	s := &peakSampler{cancel: cancel, done: make(chan struct{}), peaks: map[string]float64{}}
	go func() {
		defer close(s.done)
		for {
			if nodes, err := e.provider.ListNodes(e.cfg.Name); err == nil && len(nodes) > 0 {
				names := make([]string, len(nodes))
				for i, n := range nodes {
					names[i] = n.String()
				}
				if usage, err := dockerStats(ctx, names); err == nil {
					s.mu.Lock()
					for n, mib := range usage {
						s.peaks[n] = max(s.peaks[n], mib)
					}
					s.mu.Unlock()
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return s
}

func (s *peakSampler) stop() map[string]float64 {
	s.cancel()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.peaks)
}

func formatPeaks(peaks map[string]float64) string {
	names := slices.Sorted(maps.Keys(peaks))
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s %.0fMiB", n, peaks[n])
	}
	return strings.Join(parts, ",")
}
