package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type event struct {
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

type timing struct {
	pkg, test string
	elapsed   float64
}

type key struct{ pkg, test string }

type summary struct {
	buffered map[key][]string
	tests    []timing
	packages []timing
}

func main() {
	budget := flag.Float64("budget", 60, "seconds the whole go test run may take")
	slowest := flag.Int("slowest", 10, "number of slowest tests to print")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: testbudget [-budget seconds] [-slowest n] -- [go test flags] <packages>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	// The budget measures running tests, not compiling them: a cold build cache would otherwise decide the result.
	buildStart := time.Now()
	build := exec.Command("go", append([]string{"test", "-run", "^$"}, flag.Args()...)...)
	build.Stdout = io.Discard
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "testbudget: build tests: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("built test binaries in %.1fs (not counted)\n", time.Since(buildStart).Seconds())

	cmd := exec.Command("go", append([]string{"test", "-json"}, flag.Args()...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testbudget: %v\n", err)
		os.Exit(2)
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "testbudget: start go test: %v\n", err)
		os.Exit(2)
	}
	s := &summary{buffered: map[key][]string{}}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		s.line(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "testbudget: read go test -json output: %v\n", err)
		os.Exit(2)
	}
	testErr := cmd.Wait()
	wall := time.Since(start).Seconds()

	s.report(*slowest)
	fmt.Printf("\ntotal wall %.1fs, budget %.0fs\n", wall, *budget)
	overBudget := wall > *budget
	if overBudget {
		fmt.Fprintf(os.Stderr, "testbudget: go test took %.1fs, over the %.0fs budget (TEST_BUDGET)\n", wall, *budget)
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](testErr); ok {
		os.Exit(exitErr.ExitCode())
	}
	if testErr != nil {
		fmt.Fprintf(os.Stderr, "testbudget: go test: %v\n", testErr)
		os.Exit(2)
	}
	if overBudget {
		os.Exit(1)
	}
}

func (s *summary) line(raw []byte) {
	var ev event
	if json.Unmarshal(raw, &ev) != nil || ev.Action == "" {
		fmt.Println(string(raw))
		return
	}
	k := key{ev.Package, ev.Test}
	switch ev.Action {
	case "build-output":
		fmt.Print(ev.Output)
	case "output":
		s.buffered[k] = append(s.buffered[k], ev.Output)
	case "pass", "skip":
		delete(s.buffered, k)
		s.record(ev)
	case "fail":
		fmt.Print(strings.Join(s.buffered[k], ""))
		delete(s.buffered, k)
		s.record(ev)
	}
}

func (s *summary) record(ev event) {
	switch {
	case ev.Test == "" && ev.Action == "pass":
		fmt.Printf("ok  \t%s\t%.2fs\n", ev.Package, ev.Elapsed)
		s.packages = append(s.packages, timing{pkg: ev.Package, elapsed: ev.Elapsed})
	case ev.Test == "" && ev.Action == "fail":
		s.packages = append(s.packages, timing{pkg: ev.Package, elapsed: ev.Elapsed})
	case ev.Test != "" && !strings.Contains(ev.Test, "/"):
		s.tests = append(s.tests, timing{pkg: ev.Package, test: ev.Test, elapsed: ev.Elapsed})
	}
}

func (s *summary) report(slowest int) {
	byElapsed := func(a, b timing) int { return cmp.Compare(b.elapsed, a.elapsed) }
	slices.SortStableFunc(s.tests, byElapsed)
	slices.SortStableFunc(s.packages, byElapsed)

	fmt.Printf("\nslowest tests:\n")
	for _, t := range s.tests[:min(slowest, len(s.tests))] {
		fmt.Printf("%8.2fs  %s  %s\n", t.elapsed, t.test, t.pkg)
	}
	fmt.Printf("\npackage wall:\n")
	for _, p := range s.packages {
		fmt.Printf("%8.2fs  %s\n", p.elapsed, p.pkg)
	}
}
