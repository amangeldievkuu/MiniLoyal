package migrate_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Invariant: Roles converges. Running it again against a cluster that already
// has the roles changes nothing, keeps the single group membership, leaves the
// login role's attributes locked down and keeps the password working.
func TestRolesIsIdempotent(t *testing.T) {
	d := testutil.NewDB(t) // roles already exist after this
	ctx := context.Background()
	owner, err := sql.Open("pgx", d.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	for i := 1; i <= 2; i++ {
		if err := migrate.Roles(ctx, owner, testutil.AppPassword); err != nil {
			t.Fatalf("Roles call %d: %v", i, err)
		}
	}

	type attrs struct {
		login, super, bypass, createdb, createrole, inherit bool
	}
	read := func(role string) attrs {
		t.Helper()
		var a attrs
		if err := owner.QueryRowContext(ctx, "SELECT rolcanlogin, rolsuper, rolbypassrls, rolcreatedb, rolcreaterole, rolinherit FROM pg_roles WHERE rolname = $1", role).
			Scan(&a.login, &a.super, &a.bypass, &a.createdb, &a.createrole, &a.inherit); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		return a
	}
	if got := read(migrate.GroupRole); got != (attrs{inherit: true}) {
		t.Errorf("%s attrs = %+v, want NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE", migrate.GroupRole, got)
	}
	if got := read(migrate.LoginRole); got != (attrs{login: true, inherit: true}) {
		t.Errorf("%s attrs = %+v, want LOGIN only", migrate.LoginRole, got)
	}

	var memberships int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM pg_auth_members m
		JOIN pg_roles g ON g.oid = m.roleid JOIN pg_roles u ON u.oid = m.member
		WHERE g.rolname = $1 AND u.rolname = $2`, migrate.GroupRole, migrate.LoginRole).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Errorf("%s is a member of %s %d times, want exactly once", migrate.LoginRole, migrate.GroupRole, memberships)
	}

	// The password still works and the grants still apply after the rerun.
	pool, err := db.Connect(ctx, d.AppURL)
	if err != nil {
		t.Fatalf("login role cannot connect after Roles ran twice: %v", err)
	}
	defer pool.Close()
	tenant := d.NewTenant(t, "Alpha")
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tenants").Scan(&n); err != nil {
		t.Fatalf("select as login role: %v", err)
	}
	if n != 0 {
		t.Errorf("tenants visible without a tenant context: %d", n)
	}
	err = db.WithTenant(ctx, pool, tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM tenants").Scan(&n)
	})
	if err != nil || n != 1 {
		t.Errorf("tenant context after rerun: %d, %v", n, err)
	}

	// Up is idempotent too: nothing pending, no error.
	if err := migrate.Up(ctx, owner); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if err := migrate.Roles(ctx, owner, ""); err == nil {
		t.Error("Roles without a password must fail")
	}
}
