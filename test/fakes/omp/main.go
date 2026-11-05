package main

import (
	"os"

	"github.com/Devcon4/skua/internal/runner/agent/omprpc/omprpctest"
)

func main() {
	os.Exit(omprpctest.Main(os.Args[1:], os.Stdin, os.Stdout))
}
