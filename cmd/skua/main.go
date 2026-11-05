package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Devcon4/skua/internal/runner"
)

var commands = map[string]func(args []string) int{}

func register(name string, run func(args []string) int) {
	if _, dup := commands[name]; dup {
		panic("skua: command registered twice: " + name)
	}
	commands[name] = run
}

func main() {
	os.Exit(dispatch(os.Args, os.Stderr))
}

func dispatch(argv []string, stderr io.Writer) int {
	var args []string
	if len(argv) > 1 {
		args = argv[1:]
	}
	if len(argv) > 0 {
		if name, ok := runner.Links[filepath.Base(argv[0])]; ok {
			args = append([]string{name}, args...)
		}
	}
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage())
		return 2
	}
	run, ok := commands[args[0]]
	if !ok {
		_, _ = io.WriteString(stderr, fmt.Sprintf("skua: unknown command %q\n", args[0])+usage())
		return 2
	}
	return run(args[1:])
}

func usage() string {
	var b strings.Builder
	b.WriteString("usage: skua <command> [args]\n\ncommands:\n")
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		b.WriteString("  " + name + "\n")
	}
	return b.String()
}
