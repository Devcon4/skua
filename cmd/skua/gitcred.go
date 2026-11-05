package main

import (
	"os"
	"time"

	"github.com/Devcon4/skua/internal/runner/gitcred"
)

func init() {
	register("git-credential", func(args []string) int {
		return gitcred.Helper(args, os.Stdin, os.Stdout, os.Stderr, time.Now())
	})
	register("gh", func(args []string) int {
		return gitcred.GhShim(args, time.Now())
	})
}
