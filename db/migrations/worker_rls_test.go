package migrations_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Invariant: miniloyal_worker is NOBYPASSRLS, so row-level security binds it
// like the API role. Without a tenant or system context it sees no tenant
// rows; scoped to tenant A it sees only A's rows and cannot write B's; and
// only system mode (what the expiry, reconciliation and maintenance jobs
// use) reads across tenants. Since 00011 the tables are the ones its jobs
// read; members and apps are not among them.
func TestWorkerRoleIsBoundByRowLevelSecurity(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	tenantA, tenantB := d.NewTenant(t, "Alpha"), d.NewTenant(t, "Beta")
	apps, members := map[uuid.UUID]uuid.UUID{}, map[uuid.UUID]uuid.UUID{}
	for _, tenantID := range []uuid.UUID{tenantA, tenantB} {
		app := testutil.NewID()
		d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', $3)", app, tenantID, "cafe-"+app.String()[24:])
		m := testutil.NewID()
		d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", m, tenantID, app, "U"+m.String())
		d.Exec(t, "INSERT INTO points_accounts (member_id, tenant_id, app_id, balance) VALUES ($1, $2, $3, 5)", m, tenantID, app)
		apps[tenantID], members[tenantID] = app, m
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
	if err := d.Worker.QueryRow(ctx, "SELECT count(*) FROM points_accounts").Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if bare != 0 {
		t.Errorf("worker without a tenant context sees %d points accounts, want 0", bare)
	}

	err := db.WithTenant(ctx, d.Worker, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		if n := count(tx, "SELECT count(*) FROM points_accounts"); n != 1 {
			t.Errorf("worker scoped to A sees %d points accounts, want 1", n)
		}
		tag, err := tx.Exec(ctx, "UPDATE points_accounts SET balance = 0 WHERE tenant_id <> current_setting('app.tenant_id')::uuid")
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Errorf("worker scoped to A updated %d of B's accounts", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// An insert of another tenant's row under A's scope is refused by the
	// policy's WITH CHECK.
	err = db.WithTenant(ctx, d.Worker, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO points_ledger (tenant_id, app_id, member_id, txn_id, delta, balance_after, reason, actor_type)
			VALUES ($1, $2, $3, $4, -1, 4, 'expire', 'system')`, tenantB, apps[tenantB], members[tenantB], testutil.NewID())
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Errorf("worker inserting B's ledger row under A's scope: %v, want an RLS violation", err)
	}

	err = db.WithSystem(ctx, d.Worker, func(ctx context.Context, tx pgx.Tx) error {
		if n := count(tx, "SELECT count(*) FROM points_accounts"); n != 2 {
			t.Errorf("worker in system mode sees %d points accounts, want 2", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The ledgers and the outbox stay append-only for the worker, even in
	// system mode, and it cannot read or change what its jobs do not touch.
	d.WithSteadyWorkerRole(t, func() {
		for _, stmt := range []string{
			"UPDATE points_ledger SET delta = 0",
			"DELETE FROM points_ledger",
			"UPDATE stamp_events SET delta = 0",
			"DELETE FROM stamp_events",
			"SELECT count(*) FROM outbound_events",
			"UPDATE outbound_events SET payload = '{}'",
			"DELETE FROM outbound_events",
			"SELECT count(*) FROM audit_log",
			"UPDATE audit_log SET action = 'x'",
			"DELETE FROM audit_log",
			"SELECT count(*) FROM members",
			"UPDATE members SET display_name = 'x'",
			"SELECT count(*) FROM apps",
			"SELECT count(*) FROM app_channels",
			"SELECT count(*) FROM api_keys",
			"SELECT count(*) FROM users",
			"SELECT token_hash FROM refresh_tokens",
			"SELECT response_body FROM idempotency_keys",
			"UPDATE coupon_instances SET status = 'voided'",
			"UPDATE stamp_progress SET count = 0",
			"INSERT INTO points_lots (tenant_id, app_id, member_id, earned, remaining) VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 1, 1)",
		} {
			err := db.WithSystem(ctx, d.Worker, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, stmt)
				return err
			})
			if db.Code(err) != db.CodeInsufficientPrivilege {
				t.Errorf("worker ran %q: %v, want permission denied", stmt, err)
			}
		}
	})
}

// workerGrants is what each job needs (migration 00011): table-level
// privileges, and column-level ones where the job reads or writes only some
// columns. Every other public table, except River's, which the worker owns,
// grants it nothing.
var workerGrants = map[string]struct {
	table   []string
	columns map[string][]string
}{
	// Points expiry.
	"points_lots":     {table: []string{"SELECT"}, columns: map[string][]string{"UPDATE": {"remaining"}}},
	"points_accounts": {table: []string{"SELECT"}, columns: map[string][]string{"UPDATE": {"balance", "lifetime_earned", "updated_at"}}},
	"points_ledger":   {table: []string{"SELECT", "INSERT"}},
	"outbound_events": {table: []string{"INSERT"}},
	// Daily reconciliation.
	"stamp_progress":              {table: []string{"SELECT"}},
	"stamp_events":                {table: []string{"SELECT"}},
	"coupon_templates":            {table: []string{"SELECT"}},
	"coupon_instances":            {table: []string{"SELECT"}},
	"points_reconciliation_runs":  {table: []string{"SELECT", "INSERT", "UPDATE"}},
	"loyalty_reconciliation_runs": {table: []string{"SELECT", "INSERT", "UPDATE"}},
	// Maintenance.
	"idempotency_keys":       {table: []string{"DELETE"}, columns: map[string][]string{"SELECT": {"expires_at"}}},
	"refresh_tokens":         {table: []string{"DELETE"}, columns: map[string][]string{"SELECT": {"expires_at", "revoked_at"}}},
	"mfa_challenges":         {table: []string{"DELETE"}, columns: map[string][]string{"SELECT": {"expires_at"}}},
	"webhook_inbound_events": {table: []string{"DELETE"}, columns: map[string][]string{"SELECT": {"received_at"}}},
	"scan_token_uses":        {table: []string{"DELETE"}, columns: map[string][]string{"SELECT": {"used_at"}}},
	"scan_coupon_redeems":    {table: []string{"DELETE"}, columns: map[string][]string{"SELECT": {"used_at"}}},
}

// Phase 4 re-review R3: the worker holds exactly the grants its jobs use,
// not miniloyal_app's. The matrix walks every public table, so a new table
// that hands the worker something fails here until it is listed.
func TestWorkerRoleGrants(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	rows, err := d.Admin.Query(ctx, `SELECT relname FROM pg_class
		WHERE relkind IN ('r', 'p') AND relnamespace = 'public'::regnamespace AND relname NOT LIKE 'river\_%'
		ORDER BY relname`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}

	d.WithSteadyWorkerRole(t, func() {
		var inGroup bool
		if err := d.Admin.QueryRow(ctx, "SELECT pg_has_role($1, $2, 'MEMBER')", migrate.WorkerRole, migrate.GroupRole).Scan(&inGroup); err != nil {
			t.Fatal(err)
		}
		if inGroup {
			t.Fatalf("%s is a member of %s", migrate.WorkerRole, migrate.GroupRole)
		}
		for _, table := range tables {
			want := workerGrants[table]
			seen[table] = true
			for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
				wantTable := slices.Contains(want.table, priv)
				var got bool
				if err := d.Admin.QueryRow(ctx, "SELECT has_table_privilege($1, $2, $3)", migrate.WorkerRole, table, priv).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != wantTable {
					t.Errorf("%s on %s for %s = %v, want %v", priv, table, migrate.WorkerRole, got, wantTable)
				}
				if priv != "SELECT" && priv != "INSERT" && priv != "UPDATE" {
					continue
				}
				wantAny := wantTable || len(want.columns[priv]) > 0
				if err := d.Admin.QueryRow(ctx, "SELECT has_any_column_privilege($1, $2, $3)", migrate.WorkerRole, table, priv).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != wantAny {
					t.Errorf("%s on any column of %s for %s = %v, want %v", priv, table, migrate.WorkerRole, got, wantAny)
				}
				for _, col := range want.columns[priv] {
					if err := d.Admin.QueryRow(ctx, "SELECT has_column_privilege($1, $2, $3, $4)", migrate.WorkerRole, table, col, priv).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if !got {
						t.Errorf("%s (%s) on %s for %s missing", priv, col, table, migrate.WorkerRole)
					}
				}
			}
		}
	})
	for table := range workerGrants {
		if !seen[table] {
			t.Errorf("workerGrants lists %s, which is not a table", table)
		}
	}
}
