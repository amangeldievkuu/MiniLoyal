package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Invariant: miniloyal_worker is NOBYPASSRLS, so row-level security binds it
// like the API role. Without a tenant or system context it sees no tenant
// rows; scoped to tenant A it sees only A's rows and cannot write B's; and
// only system mode (what the expiry and maintenance jobs use) reads across
// tenants.
func TestWorkerRoleIsBoundByRowLevelSecurity(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	tenantA, tenantB := d.NewTenant(t, "Alpha"), d.NewTenant(t, "Beta")
	for _, tenantID := range []any{tenantA, tenantB} {
		app := testutil.NewID()
		d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', $3)", app, tenantID, "cafe-"+app.String()[24:])
		m := testutil.NewID()
		d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", m, tenantID, app, "U"+m.String())
	}

	count := func(tx pgx.Tx, q string) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}

	// No context at all.
	var bare int
	if err := d.Worker.QueryRow(ctx, "SELECT count(*) FROM members").Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if bare != 0 {
		t.Errorf("worker without a tenant context sees %d members, want 0", bare)
	}

	err := db.WithTenant(ctx, d.Worker, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		if n := count(tx, "SELECT count(*) FROM members"); n != 1 {
			t.Errorf("worker scoped to A sees %d members, want 1", n)
		}
		if n := count(tx, "SELECT count(*) FROM apps"); n != 1 {
			t.Errorf("worker scoped to A sees %d apps, want 1", n)
		}
		tag, err := tx.Exec(ctx, "UPDATE members SET display_name = 'x' WHERE tenant_id <> current_setting('app.tenant_id')::uuid")
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Errorf("worker scoped to A updated %d of B's members", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// An insert of another tenant's row under A's scope is refused by the
	// policy's WITH CHECK.
	err = db.WithTenant(ctx, d.Worker, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Evil', 'evil')", testutil.NewID(), tenantB)
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Errorf("worker inserting B's row under A's scope: %v, want an RLS violation", err)
	}

	err = db.WithSystem(ctx, d.Worker, func(ctx context.Context, tx pgx.Tx) error {
		if n := count(tx, "SELECT count(*) FROM members"); n != 2 {
			t.Errorf("worker in system mode sees %d members, want 2", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The ledgers and the outbox stay append-only for the worker too, even in
	// system mode: it inherits miniloyal_app's SELECT and INSERT and nothing
	// more.
	for _, stmt := range []string{
		"UPDATE points_ledger SET delta = 0",
		"DELETE FROM points_ledger",
		"UPDATE stamp_events SET delta = 0",
		"DELETE FROM stamp_events",
		"UPDATE outbound_events SET payload = '{}'",
		"DELETE FROM outbound_events",
		"UPDATE audit_log SET action = 'x'",
		"DELETE FROM audit_log",
	} {
		err := db.WithSystem(ctx, d.Worker, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		if db.Code(err) != db.CodeInsufficientPrivilege {
			t.Errorf("worker ran %q: %v, want permission denied", stmt, err)
		}
	}
}
