package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/operator"
)

func init() {
	register("operator", runOperator)
}

func runOperator(args []string) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skua operator: %v\n", err)
		return 1
	}
	ctrl.SetLogger(zap.New())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := operator.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "skua operator: %v\n", err)
		return 1
	}
	return 0
}
