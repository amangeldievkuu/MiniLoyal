// Command worker runs background jobs on River: hourly maintenance that keeps
// the identity and webhook tables bounded, points expiry every 15 minutes and
// the daily points reconciliation. Several workers may run at once; River's
// leader election schedules each periodic job once.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/amangeldievkuu/miniloyal/internal/jobs"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
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

	deps := jobs.Deps{Pool: pool, Clock: clock.Real{}, Log: logger}
	// INTEGRATOR: pass the points service here once internal/points lands:
	// deps.Expirer, deps.Reconciler = pointsSvc, pointsSvc

	client, err := jobs.NewClient(pool, deps, cfg.WorkerConcurrency)
	if err != nil {
		logger.Error("job client setup failed", "err", err)
		return 1
	}
	if err := client.Start(ctx); err != nil {
		logger.Error("job client start failed", "err", err)
		return 1
	}
	logger.Info("worker started", "concurrency", cfg.WorkerConcurrency, "version", version)

	<-ctx.Done()
	logger.Info("worker stopping")
	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := client.StopAndCancel(stopCtx); err != nil {
		logger.Error("job client stop failed", "err", err)
		return 1
	}
	logger.Info("worker stopped")
	return 0
}
