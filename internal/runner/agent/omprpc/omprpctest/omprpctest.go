// Package omprpctest is a scripted stand-in for `omp --mode rpc`. It speaks the subset of
// omp's newline-delimited JSON RPC that the omp-rpc adapter uses, and takes its
// behaviour from prompts of the form `SCRIPT:<name> <args>`.
package omprpctest

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	maxFrameBytes            = 1 << 20
	maxReassembledFrameBytes = 64 << 20
	chunkSegmentBytes        = 512 << 10
	sessionStateFile         = "fakeomp-session.json"
)

// Main runs the fake agent with omp's command-line args, reading commands from stdin and
// writing frames to stdout. It returns the process exit code: 0 after stdin closes, 1 for
// --exit-before-ready or a scripted crash, 2 for bad flags.
func Main(args []string, stdin io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("fakeomp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("mode", "", "transport mode; only rpc is supported")
	versions := fs.String("protocol-versions", "1,2", "comma-separated RPC protocol versions offered in ready")
	exitBeforeReady := fs.Bool("exit-before-ready", false, "exit 1 before writing the ready frame")
	listen := fs.String("listen", "", "address of an HTTP server that answers ok")
	settleDelay := fs.Duration("settle-delay", 0, "delay between an error prompt_result and its session_settled frame")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mode != "rpc" {
		fmt.Fprintln(os.Stderr, "fakeomp: --mode rpc is required")
		return 2
	}
	supported, err := parseVersions(*versions)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeomp:", err)
		return 2
	}
	if *exitBeforeReady {
		fmt.Fprintln(os.Stderr, "fakeomp: exiting before ready")
		return 1
	}
	if *listen != "" {
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeomp:", err)
			return 1
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()
	}
	a := &agent{
		out:         &frameWriter{w: stdout},
		supported:   supported,
		settleDelay: *settleDelay,
		session:     &session{state: sessionState{}},
		exit:        make(chan int, 1),
	}
	return a.serve(stdin)
}

func parseVersions(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil, fmt.Errorf("bad --protocol-versions %q", s)
		}
		out = append(out, v)
	}
	return out, nil
}
