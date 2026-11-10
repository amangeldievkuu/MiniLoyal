package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Security review Phase 3, F7: the application cannot delete apps, and no
// delete cascades through the points history.
func TestPointsHistoryCannotBeDeletedByCascade(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	tenantID := d.NewTenant(t, "Alpha")
	appID, memberID := testutil.NewID(), testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", appID, tenantID)
	d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, 'U1')", memberID, tenantID, appID)
	var lot int64
	if err := d.Admin.QueryRow(ctx, "INSERT INTO points_lots (tenant_id, app_id, member_id, earned, remaining) VALUES ($1, $2, $3, 5, 5) RETURNING id", tenantID, appID, memberID).Scan(&lot); err != nil {
		t.Fatal(err)
	}
	d.Exec(t, "INSERT INTO points_ledger (tenant_id, app_id, member_id, txn_id, lot_id, delta, balance_after, reason, actor_type) VALUES ($1, $2, $3, $4, $5, 5, 5, 'earn', 'system')", tenantID, appID, memberID, testutil.NewID(), lot)

	err := d.WithTenant(t, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "DELETE FROM apps WHERE id = $1", appID)
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Fatalf("application deleting an app: %v, want permission denied", err)
	}
	for _, stmt := range []string{"DELETE FROM members WHERE id = $1", "DELETE FROM apps WHERE id = $1"} {
		id := memberID
		if stmt == "DELETE FROM apps WHERE id = $1" {
			id = appID
		}
		if _, err := d.Admin.Exec(ctx, stmt, id); db.Code(err) != db.CodeForeignKeyViolation {
			t.Errorf("%s with points history: %v, want a foreign key violation", stmt, err)
		}
	}
	var rows int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_ledger WHERE member_id = $1", memberID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("ledger rows = %d (%v), want 1", rows, err)
	}
}

// Security review Phase 3, F8: reconciliation runs name members of every
// tenant, so only system mode reads or writes them.
func TestReconciliationRunsAreSystemOnly(t *testing.T) {
	d := testutil.NewDB(t)
	tenantID := d.NewTenant(t, "Alpha")
	d.Exec(t, "INSERT INTO points_reconciliation_runs (started_at, details) VALUES (now(), '[{\"member_id\":\"x\"}]')")

	err := d.WithTenant(t, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM points_reconciliation_runs").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("tenant context sees %d reconciliation runs", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = d.WithTenant(t, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO points_reconciliation_runs (started_at) VALUES (now())")
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Fatalf("tenant context writing a run: %v, want refused", err)
	}
	err = d.WithSystem(t, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM points_reconciliation_runs").Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("system mode sees %d runs, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
