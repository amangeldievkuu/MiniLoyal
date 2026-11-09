package webhook

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

// DeleteOldInboundEvents drops deduplication records older than seven days.
// The worker runs it periodically.
func DeleteOldInboundEvents(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := db.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		n, err = sqlc.New(tx).DeleteOldInboundEvents(ctx)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("webhook: delete old events: %w", err)
	}
	return n, nil
}
