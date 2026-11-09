// Package db opens the PostgreSQL connection pool shared by the services and
// scopes transactions to a tenant so row-level security applies.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pool for url and verifies it with a ping.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: parse DATABASE_URL: %w", err)
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	if cfg.MaxConns < 8 {
		cfg.MaxConns = 8
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// TxFunc runs inside a transaction. Returning an error rolls it back.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// WithTenant runs fn in a transaction whose row-level security context is
// tenantID: policies show and accept only that tenant's rows. Every
// tenant-scoped query goes through here.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, fn TxFunc) error {
	if tenantID == uuid.Nil {
		return errors.New("db: WithTenant needs a tenant ID")
	}
	return inTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String()); err != nil {
			return fmt.Errorf("db: set tenant context: %w", err)
		}
		return fn(ctx, tx)
	})
}

// WithSystem runs fn in a transaction that bypasses tenant policies. It exists
// for the few lookups that happen before the tenant is known: sign-in by
// email, API key lookup by hash, webhook routing by channel. Keep fn small and
// never pass request-controlled filters into it beyond the lookup key.
func WithSystem(ctx context.Context, pool *pgxpool.Pool, fn TxFunc) error {
	return inTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.system', 'on', true)"); err != nil {
			return fmt.Errorf("db: set system context: %w", err)
		}
		return fn(ctx, tx)
	})
}

func inTx(ctx context.Context, pool *pgxpool.Pool, fn TxFunc) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// Postgres error codes the services react to.
const (
	CodeUniqueViolation       = "23505"
	CodeForeignKeyViolation   = "23503"
	CodeInsufficientPrivilege = "42501" // also raised by a failed RLS WITH CHECK
	CodeCheckViolation        = "23514"
)

// Code returns the SQLSTATE of err, or "" when err is not a Postgres error.
func Code(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// IsUniqueViolation reports whether err is a unique constraint violation,
// optionally on the named constraint or index.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != CodeUniqueViolation {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// IsNotFound reports whether err is pgx.ErrNoRows.
func IsNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
