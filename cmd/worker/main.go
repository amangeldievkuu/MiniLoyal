// Command worker runs background jobs (messages, points expiry, webhook
// deliveries, reconciliation). Job handlers arrive with their features; this
// binary already owns configuration, logging, tracing and the database pool.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/log"
	"github.com/amangeldievkuu/miniloyal/internal/platform/otel"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	logger, err := log.Setup(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := otel.Setup(ctx, "miniloyal-worker", version, cfg.OTLPEndpoint)
	if err != nil {
		logger.Error("tracing setup failed", "err", err)
		return 1
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "err", err)
		return 1
	}
	defer pool.Close()

	logger.Info("worker started", "concurrency", cfg.WorkerConcurrency, "version", version, "jobs", 0)
	<-ctx.Done()
	logger.Info("worker stopping")
	return 0
}
