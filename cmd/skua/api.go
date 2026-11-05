package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/Devcon4/skua/internal/api"
	"github.com/Devcon4/skua/internal/contract/config"
)

func init() {
	register("api", runAPI)
}

func runAPI(args []string) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skua api: %v\n", err)
		return 1
	}
	ctrl.SetLogger(zap.New())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := api.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "skua api: %v\n", err)
		return 1
	}
	return 0
}
