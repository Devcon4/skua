package main

import (
	"fmt"
	"os"

	"github.com/Devcon4/skua/internal/version"
)

func init() {
	register("version", runVersion)
}

func runVersion(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: skua version")
		return 2
	}
	fmt.Println(version.Version)
	return 0
}
