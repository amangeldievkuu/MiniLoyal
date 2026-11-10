// Command worker runs background jobs on River: hourly maintenance that keeps
// the identity and webhook tables bounded, points expiry every 15 minutes and
// the daily reconciliation of points, stamps and coupons. Several workers
// may run at once; River's leader election schedules each periodic job once.
//
// It connects with WORKER_DATABASE_URL as miniloyal_worker, the role that owns
// River's tables. That role cannot bypass row-level security: jobs that span
// tenants use system mode, exactly as they did under the API role.
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
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/stamp"
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

	// The worker connects as miniloyal_worker, which owns River's tables; the
	// API's role may only insert jobs.
	if cfg.WorkerDatabaseURL == "" {
		logger.Error("WORKER_DATABASE_URL is required")
		return 2
	}
	pool, err := db.Connect(ctx, cfg.WorkerDatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "err", err)
		return 1
	}
	defer pool.Close()
	if err := jobs.CheckWorkerRole(ctx, pool); err != nil {
		logger.Error("worker database role rejected", "err", err)
		return 1
	}

	deps := jobs.Deps{Pool: pool, Clock: clock.Real{}, Log: logger}
	// Expiry and reconciliation need only the pool, clock and logger; the
	// HTTP-facing dependencies (tenants, keyring, card) stay nil here.
	pointsSvc := points.New(points.Deps{Pool: pool, Clock: clock.Real{}, Log: logger})
	deps.Expirer, deps.Reconciler = pointsSvc, pointsSvc
	deps.Loyalty = stamp.New(stamp.Deps{Pool: pool, Clock: clock.Real{}, Log: logger, StaffWritesPerMinute: -1})

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
