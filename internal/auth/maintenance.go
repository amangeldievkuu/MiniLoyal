package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
)

// SweepSpentRefreshTokens deletes expired and long-revoked refresh tokens for
// every account. The worker runs it periodically; Service.SweepAll is the
// same operation for callers that already hold a Service.
func SweepSpentRefreshTokens(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	n, err := sqlc.New(pool).DeleteSpentRefreshTokens(ctx)
	if err != nil {
		return 0, fmt.Errorf("auth: sweep refresh tokens: %w", err)
	}
	return n, nil
}

// DeleteExpiredMFAChallenges removes MFA challenge rows an hour past expiry.
func DeleteExpiredMFAChallenges(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	n, err := sqlc.New(pool).DeleteExpiredMfaChallenges(ctx)
	if err != nil {
		return 0, fmt.Errorf("auth: delete mfa challenges: %w", err)
	}
	return n, nil
}
