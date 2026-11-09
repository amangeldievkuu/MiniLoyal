package migrations_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
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

// ---- every tenant table, data-driven

// execer is satisfied by pgx.Tx and *pgxpool.Pool, so the same insert runs
// as the owner (fixtures) and as the application (attacks).
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// rlsFixture holds tenant A's dependencies for the inserts below.
type rlsFixture struct {
	d      *testutil.DB
	t      *testing.T
	tenant uuid.UUID
	app    uuid.UUID
}

// user inserts a fresh dashboard user as the owner and returns its ID.
func (f rlsFixture) user() uuid.UUID {
	id := testutil.NewID()
	f.d.Exec(f.t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", id, randHex(6)+"@example.com")
	return id
}

// channelEnv hands out the three channel environments in turn, so repeated
// inserts for one app do not collide on (app_id, environment).
var channelSeq atomic.Int32

func channelEnv() string {
	return []string{"developing", "review", "published"}[int(channelSeq.Add(1)-1)%3]
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// tenantTable describes one tenant-scoped table.
type tenantTable struct {
	name string
	// col is the column that scopes rows to a tenant.
	col string
	// insert adds one row that belongs to f.tenant. For tenants itself the
	// row's id is a fresh tenant, since a tenant cannot own another row.
	insert func(ctx context.Context, x execer, f rlsFixture) error
	// seeded is false when tenant A's row exists before the test (tenants).
	seeded bool
	// appDelete is true when the application role may DELETE from the table.
	appDelete bool
}

var rlsTables = []tenantTable{
	{name: "tenants", col: "id", insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO tenants (id, type, name, slug) VALUES ($1, 'brand', 'Sneaky', $2)", testutil.NewID(), "sneaky-"+randHex(3))
		return err
	}},
	{name: "tenant_users", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'staff')", f.tenant, f.user())
		return err
	}},
	{name: "apps", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'App', $3)", testutil.NewID(), f.tenant, "app-"+randHex(3))
		return err
	}},
	{name: "app_channels", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO app_channels (id, tenant_id, app_id, environment, channel_id, liff_id) VALUES ($1, $2, $3, $4, '2007000001', $5)",
			testutil.NewID(), f.tenant, f.app, channelEnv(), "2007000001-"+randHex(4))
		return err
	}},
	{name: "stores", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO stores (id, tenant_id, app_id, name) VALUES ($1, $2, $3, 'Main')", testutil.NewID(), f.tenant, f.app)
		return err
	}},
	{name: "staff", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO staff (id, tenant_id, app_id, display_name) VALUES ($1, $2, $3, 'Mei')", testutil.NewID(), f.tenant, f.app)
		return err
	}},
	{name: "api_keys", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO api_keys (id, tenant_id, app_id, name, prefix, key_hash, scopes) VALUES ($1, $2, $3, 'pos', 'ml_abcdefgh', $4, '{members:read}')",
			testutil.NewID(), f.tenant, f.app, randHex(32))
		return err
	}},
	{name: "audit_log", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO audit_log (tenant_id, app_id, actor_type, action, target_type) VALUES ($1, $2, 'system', 'test', 'test')", f.tenant, f.app)
		return err
	}},
	{name: "idempotency_keys", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, expires_at) VALUES ($1, 'user:x', $2, '\\x00', now() + interval '1 day')", f.tenant, randHex(8))
		return err
	}},
}

// TestEveryTenantTableIsListed keeps rlsTables in step with the schema: a
// new tenant table fails here until it gets an entry.
func TestEveryTenantTableIsListed(t *testing.T) {
	d := testutil.NewDB(t)
	rows, err := d.Admin.Query(context.Background(),
		"SELECT relname FROM pg_class WHERE relkind = 'r' AND relnamespace = 'public'::regnamespace AND relrowsecurity ORDER BY relname")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	listed := map[string]bool{}
	for _, tbl := range rlsTables {
		listed[tbl.name] = true
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !listed[name] {
			t.Errorf("table %s has row-level security but no entry in rlsTables", name)
		}
		delete(listed, name)
	}
	for name := range listed {
		t.Errorf("rlsTables entry %s is not a table with row-level security", name)
	}
}

// Invariant: in every tenant table, tenant A's rows are invisible from
// tenant B's context and from no context; a row claiming tenant A cannot be
// inserted, moved or deleted from tenant B's context.
func TestEveryTenantTableIsIsolated(t *testing.T) {
	d := testutil.NewDB(t)
	tenantA := d.NewTenant(t, "Alpha")
	tenantB := d.NewTenant(t, "Beta")
	appA := testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", appA, tenantA)
	f := rlsFixture{d: d, t: t, tenant: tenantA, app: appA}
	ctx := context.Background()

	countAs := func(t *testing.T, tbl tenantTable, run func(fn db.TxFunc) error) int {
		t.Helper()
		var n int
		err := run(func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s: %v", tbl.name, err)
		}
		return n
	}
	ownerCount := func(t *testing.T, tbl tenantTable) int {
		t.Helper()
		var n int
		if err := d.Admin.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, tbl := range rlsTables {
		t.Run(tbl.name, func(t *testing.T) {
			if tbl.seeded {
				if err := tbl.insert(ctx, d.Admin, f); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			before := ownerCount(t, tbl)
			if before == 0 {
				t.Fatal("fixture missing")
			}

			inA := countAs(t, tbl, func(fn db.TxFunc) error { return d.WithTenant(t, tenantA, fn) })
			inB := countAs(t, tbl, func(fn db.TxFunc) error { return d.WithTenant(t, tenantB, fn) })
			system := countAs(t, tbl, func(fn db.TxFunc) error { return d.WithSystem(t, fn) })
			var noCtx int
			if err := d.App.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA).Scan(&noCtx); err != nil {
				t.Fatal(err)
			}
			if inA != before || system != before {
				t.Errorf("own context sees %d, system sees %d, want %d", inA, system, before)
			}
			if inB != 0 || noCtx != 0 {
				t.Errorf("tenant B sees %d, no context sees %d, want 0", inB, noCtx)
			}

			// Insert claiming tenant A from tenant B's context.
			err := d.WithTenant(t, tenantB, func(ctx context.Context, tx pgx.Tx) error { return tbl.insert(ctx, tx, f) })
			if db.Code(err) != db.CodeInsufficientPrivilege {
				t.Errorf("cross-tenant insert: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
			}
			// The same insert from tenant A's own context is allowed (unless
			// the table is one the application never inserts into this way).
			if tbl.name != "tenants" {
				if err := d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error { return tbl.insert(ctx, tx, f) }); err != nil {
					t.Errorf("own-tenant insert: %v", err)
				}
				before++
			}

			// Moving tenant A's rows to tenant B from A's context.
			if tbl.col == "tenant_id" {
				err := d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET tenant_id = $2 WHERE tenant_id = $1", tbl.name), tenantA, tenantB)
					return err
				})
				if db.Code(err) != db.CodeInsufficientPrivilege {
					t.Errorf("move to another tenant: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
				}
			}

			// Deleting tenant A's rows from tenant B's context: either the
			// rows are invisible (0 affected) or the role lacks DELETE.
			err = d.WithTenant(t, tenantB, func(ctx context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 0 {
					return fmt.Errorf("deleted %d rows of tenant A", tag.RowsAffected())
				}
				return nil
			})
			switch {
			case tbl.appDelete && err != nil:
				t.Errorf("cross-tenant delete: %v", err)
			case !tbl.appDelete && db.Code(err) != db.CodeInsufficientPrivilege:
				t.Errorf("delete without privilege: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
			}

			if after := ownerCount(t, tbl); after != before {
				t.Errorf("tenant A rows went from %d to %d", before, after)
			}
		})
	}
}

// Invariant: the application roles hold exactly the DML grants the schema
// intends. Append-only tables cannot be changed or emptied; API keys are
// revoked, never deleted; nothing may be truncated.
func TestApplicationRoleGrants(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	cases := []struct {
		table, priv string
		want        bool
	}{
		{"audit_log", "SELECT", true},
		{"audit_log", "INSERT", true},
		{"audit_log", "UPDATE", false},
		{"audit_log", "DELETE", false},
		{"api_keys", "SELECT", true},
		{"api_keys", "INSERT", true},
		{"api_keys", "UPDATE", true},
		{"api_keys", "DELETE", false},
		{"tenants", "DELETE", false},
		{"tenants", "UPDATE", true},
		{"idempotency_keys", "DELETE", true},
		{"refresh_tokens", "DELETE", true},
		{"goose_db_version", "SELECT", false},
		{"goose_db_version", "INSERT", false},
	}
	for _, role := range []string{migrate.GroupRole, migrate.LoginRole} {
		for _, tc := range cases {
			var got bool
			if err := d.Admin.QueryRow(ctx, "SELECT has_table_privilege($1, $2, $3)", role, tc.table, tc.priv).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("%s on %s for %s = %v, want %v", tc.priv, tc.table, role, got, tc.want)
			}
		}
	}

	// No table grants TRUNCATE, TRIGGER or REFERENCES to the application.
	rows, err := d.Admin.Query(ctx, "SELECT relname FROM pg_class WHERE relkind = 'r' AND relnamespace = 'public'::regnamespace")
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
	for _, table := range tables {
		for _, priv := range []string{"TRUNCATE", "TRIGGER", "REFERENCES"} {
			var got bool
			if err := d.Admin.QueryRow(ctx, "SELECT has_table_privilege($1, $2, $3)", migrate.LoginRole, table, priv).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got {
				t.Errorf("%s may %s %s", migrate.LoginRole, priv, table)
			}
		}
	}
	// The application owns nothing, so it cannot ALTER a table or drop a policy.
	var owned int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner WHERE c.relnamespace = 'public'::regnamespace AND r.rolname IN ($1, $2)", migrate.GroupRole, migrate.LoginRole).Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if owned != 0 {
		t.Errorf("application roles own %d relations", owned)
	}
}
