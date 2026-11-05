package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/utils/clock"

	"github.com/Devcon4/skua/internal/integration/litellm/litellmtest"
)

//go:embed scenarios/*.yaml
var scenarioFiles embed.FS

func main() {
	scenarios, err := litellmtest.LoadFS(scenarioFiles, "scenarios/*.yaml")
	if err != nil {
		log.Fatalf("fakellm: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           litellmtest.NewServer(scenarios, clock.RealClock{}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("fakellm: serving %d scenarios on %s", len(scenarios), srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("fakellm: %v", err)
	}
}
