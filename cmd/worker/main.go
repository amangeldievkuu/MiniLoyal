// Command worker runs background jobs. Feature jobs (messages, points expiry,
// webhook deliveries, reconciliation) arrive with their phases; today it runs
// the periodic maintenance that keeps the identity tables bounded.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/log"
	"github.com/amangeldievkuu/miniloyal/internal/platform/otel"
)

var version = "dev"

// maintenanceInterval is how often spent tokens, expired idempotency
// outcomes and old MFA challenges are deleted.
const maintenanceInterval = time.Hour

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

	logger.Info("worker started", "concurrency", cfg.WorkerConcurrency, "version", version, "jobs", 1)
	runMaintenance(ctx, pool, logger)
	logger.Info("worker stopping")
	return 0
}

// runMaintenance runs the cleanup once at start and then every interval
// until ctx is cancelled.
func runMaintenance(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		maintain(ctx, pool, logger)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func maintain(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	store := idempotency.New(pool, nil, nil)
	idem, err := store.DeleteExpired(ctx)
	if err != nil {
		logger.Error("maintenance: idempotency cleanup failed", "err", err)
	}
	tokens, err := auth.SweepSpentRefreshTokens(ctx, pool)
	if err != nil {
		logger.Error("maintenance: refresh token sweep failed", "err", err)
	}
	challenges, err := auth.DeleteExpiredMFAChallenges(ctx, pool)
	if err != nil {
		logger.Error("maintenance: mfa challenge cleanup failed", "err", err)
	}
	logger.Info("maintenance run", "idempotency_keys", idem, "refresh_tokens", tokens, "mfa_challenges", challenges)
}
