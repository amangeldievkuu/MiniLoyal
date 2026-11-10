package points

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

// errLotDone means another run already expired the lot.
var errLotDone = errors.New("points: lot already expired")

// ExpireDue expires up to batch lots whose expiry passed before now, oldest
// first, each in its own tenant transaction. It is safe to run twice and to
// run concurrently: the lot is re-read under a row lock and the ledger
// allows one expire row per lot. It returns how many lots it expired.
func (s *Service) ExpireDue(ctx context.Context, now time.Time, batch int) (int, error) {
	if batch < 1 {
		batch = 500
	}
	var due []sqlc.PointsLot
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		due, err = sqlc.New(tx).ListDueLots(ctx, sqlc.ListDueLotsParams{Now: pgtype.Timestamptz{Time: now, Valid: true}, Batch: int32(batch)})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("points: due lots: %w", err)
	}
	expired := 0
	for _, lot := range due {
		err := db.WithTenant(ctx, s.pool, lot.TenantID, func(ctx context.Context, tx pgx.Tx) error {
			return s.expireLot(ctx, sqlc.New(tx), lot, now)
		})
		switch {
		case err == nil:
			expired++
		case errors.Is(err, errLotDone), db.IsUniqueViolation(err, "points_ledger_expire_once_idx"):
		default:
			return expired, fmt.Errorf("points: expire lot %d: %w", lot.ID, err)
		}
	}
	if expired > 0 {
		s.log.InfoContext(ctx, "points expired", "lots", expired)
	}
	return expired, nil
}

func (s *Service) expireLot(ctx context.Context, q *sqlc.Queries, due sqlc.PointsLot, now time.Time) error {
	acct, err := q.LockPointsAccount(ctx, due.MemberID)
	if err != nil {
		return fmt.Errorf("lock account: %w", err)
	}
	lot, err := q.LockPointsLot(ctx, due.ID)
	if err != nil {
		return fmt.Errorf("lock lot: %w", err)
	}
	if lot.Remaining == 0 || !lot.ExpiresAt.Valid || lot.ExpiresAt.Time.After(now) {
		return errLotDone
	}
	balance := acct.Balance - int64(lot.Remaining)
	if balance < 0 {
		return fmt.Errorf("member %s balance %d is below the lot's %d remaining points", lot.MemberID, acct.Balance, lot.Remaining)
	}
	lotID := lot.ID
	if _, err := q.InsertLedgerEntry(ctx, sqlc.InsertLedgerEntryParams{
		TenantID: lot.TenantID, AppID: lot.AppID, MemberID: lot.MemberID,
		TxnID: uuid.Must(uuid.NewV7()), LotID: &lotID, Delta: -lot.Remaining, BalanceAfter: balance,
		Reason: KindExpire, ActorType: "system",
	}); err != nil {
		return err
	}
	if err := q.SetLotRemaining(ctx, sqlc.SetLotRemainingParams{ID: lot.ID, Remaining: 0}); err != nil {
		return fmt.Errorf("empty lot: %w", err)
	}
	if err := q.SetPointsBalance(ctx, sqlc.SetPointsBalanceParams{MemberID: lot.MemberID, Balance: balance}); err != nil {
		return fmt.Errorf("set balance: %w", err)
	}
	return nil
}

// mismatchDetail is one entry of a reconciliation run's details.
type mismatchDetail struct {
	MemberID      uuid.UUID `json:"member_id"`
	AppID         uuid.UUID `json:"app_id"`
	Balance       int64     `json:"balance"`
	LedgerSum     int64     `json:"ledger_sum"`
	LotsRemaining int64     `json:"lots_remaining"`
}

// maxDetails bounds what one run records.
const maxDetails = 100

// Reconcile checks every account: the cached balance must equal the sum of
// the ledger and the points left in open lots. The run is recorded in
// points_reconciliation_runs; mismatches are logged at ERROR.
func (s *Service) Reconcile(ctx context.Context, now time.Time) (checked int, mismatches int, err error) {
	err = db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		runID, err := q.StartReconciliationRun(ctx, pgtype.Timestamptz{Time: now, Valid: true})
		if err != nil {
			return fmt.Errorf("start run: %w", err)
		}
		n, err := q.CountPointsAccounts(ctx)
		if err != nil {
			return fmt.Errorf("count accounts: %w", err)
		}
		rows, err := q.ReconcilePointsAccounts(ctx)
		if err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		details := make([]mismatchDetail, 0, min(len(rows), maxDetails))
		for _, r := range rows {
			if len(details) == maxDetails {
				break
			}
			details = append(details, mismatchDetail{MemberID: r.MemberID, AppID: r.AppID, Balance: r.Balance, LedgerSum: r.LedgerSum, LotsRemaining: r.LotsRemaining})
		}
		raw, err := json.Marshal(details)
		if err != nil {
			return err
		}
		checked, mismatches = int(n), len(rows)
		return q.FinishReconciliationRun(ctx, sqlc.FinishReconciliationRunParams{
			ID: runID, FinishedAt: pgtype.Timestamptz{Time: s.clock.Now(), Valid: true},
			AccountsChecked: int32(checked), Mismatches: int32(mismatches), Details: raw,
		})
	})
	if err != nil {
		return 0, 0, fmt.Errorf("points: reconciliation: %w", err)
	}
	if mismatches > 0 {
		s.log.ErrorContext(ctx, "points reconciliation found mismatches", "accounts_checked", checked, "mismatches", mismatches)
	} else {
		s.log.InfoContext(ctx, "points reconciliation clean", "accounts_checked", checked)
	}
	return checked, mismatches, nil
}
