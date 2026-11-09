package migrations_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// countApps counts rows visible to the current transaction.
func countApps(ctx context.Context, tx pgx.Tx) (int, error) {
	var n int
	err := tx.QueryRow(ctx, "SELECT count(*) FROM apps").Scan(&n)
	return n, err
}

func TestTenantIsolationOnApps(t *testing.T) {
	d := testutil.NewDB(t)
	tenantA := d.NewTenant(t, "Alpha")
	tenantB := d.NewTenant(t, "Beta")

	appID := testutil.NewID()
	err := d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", appID, tenantA)
		return err
	})
	if err != nil {
		t.Fatalf("insert as tenant A: %v", err)
	}

	cases := []struct {
		name   string
		tenant uuid.UUID
		want   int
	}{
		{"own tenant sees its app", tenantA, 1},
		{"other tenant sees nothing", tenantB, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			err := d.WithTenant(t, tc.tenant, func(ctx context.Context, tx pgx.Tx) (err error) {
				got, err = countApps(ctx, tx)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("no tenant context sees nothing", func(t *testing.T) {
		var got int
		err := d.App.QueryRow(context.Background(), "SELECT count(*) FROM apps").Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Errorf("count without context = %d, want 0", got)
		}
	})

	t.Run("system context sees everything", func(t *testing.T) {
		var got int
		err := d.WithSystem(t, func(ctx context.Context, tx pgx.Tx) (err error) {
			got, err = countApps(ctx, tx)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != 1 {
			t.Errorf("system count = %d, want 1", got)
		}
	})
}

func TestWithCheckRejectsForeignTenantRows(t *testing.T) {
	d := testutil.NewDB(t)
	tenantA := d.NewTenant(t, "Alpha")
	tenantB := d.NewTenant(t, "Beta")

	err := d.WithTenant(t, tenantB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Sneaky', 'sneaky')", testutil.NewID(), tenantA)
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Fatalf("insert for another tenant: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
	}

	// Updates cannot move a row to another tenant either.
	appID := testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", appID, tenantA)
	err = d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE apps SET tenant_id = $2 WHERE id = $1", appID, tenantB)
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Fatalf("move row to another tenant: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
	}
}

func TestAuditLogIsAppendOnlyForTheApplication(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")

	var id int64
	err := d.WithTenant(t, tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			"INSERT INTO audit_log (tenant_id, actor_type, action, target_type) VALUES ($1, 'system', 'test.insert', 'test') RETURNING id",
			tenant).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, stmt := range []string{
		"UPDATE audit_log SET action = 'tampered' WHERE id = $1",
		"DELETE FROM audit_log WHERE id = $1",
	} {
		err := d.WithTenant(t, tenant, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, id)
			return err
		})
		if db.Code(err) != db.CodeInsufficientPrivilege {
			t.Errorf("%s: got %v, want permission denied", stmt, err)
		}
	}
}

func TestApplicationRoleCannotBypassRLS(t *testing.T) {
	d := testutil.NewDB(t)
	var super, bypass bool
	err := d.App.QueryRow(context.Background(),
		"SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&super, &bypass)
	if err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Fatalf("application role must not be superuser (%v) or bypass RLS (%v)", super, bypass)
	}

	// Policies are forced, so even the table owner would be subject to them.
	rows, err := d.Admin.Query(context.Background(),
		"SELECT relname FROM pg_class WHERE relkind = 'r' AND relnamespace = 'public'::regnamespace AND relname NOT IN ('goose_db_version', 'users', 'refresh_tokens', 'totp_recovery_codes', 'mfa_challenges') AND NOT (relrowsecurity AND relforcerowsecurity)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var unprotected []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		unprotected = append(unprotected, name)
	}
	if len(unprotected) > 0 {
		t.Errorf("tables without forced row-level security: %v", unprotected)
	}
}

func TestWithTenantRejectsNilTenant(t *testing.T) {
	d := testutil.NewDB(t)
	err := d.WithTenant(t, uuid.Nil, func(context.Context, pgx.Tx) error { return nil })
	if err == nil {
		t.Fatal("expected an error for the nil tenant")
	}
}
