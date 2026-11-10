// Package migrate applies the embedded goose migrations and creates the
// database roles the application connects with. The migrate binary and the
// test utilities both use it.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/amangeldievkuu/miniloyal/db/migrations"
)

// Role names. GroupRole owns no tables and carries the grants on tenant
// tables. LoginRole is what the API connects as and is a member of
// GroupRole. WorkerRole is what the worker connects as: it owns River's job
// tables (migration 00009) and holds explicit grants for what its jobs touch
// (migration 00011), not GroupRole's. Neither bypasses row-level security.
const (
	GroupRole  = "miniloyal_app"
	LoginRole  = "miniloyal_api"
	WorkerRole = "miniloyal_worker"
)

// Passwords are the login roles' passwords that Roles sets.
type Passwords struct {
	API    string // LoginRole, from APP_DB_PASSWORD
	Worker string // WorkerRole, from WORKER_DB_PASSWORD
}

// rolesLockKey serializes concurrent Roles calls within one database.
// Roles are cluster-wide, so callers connected to different databases can
// still collide; Postgres then fails with "tuple concurrently updated" or
// "role already exists", and Roles retries.
const (
	rolesLockKey  = 0x4d4c524f // "MLRO"
	rolesAttempts = 8
)

// Roles creates GroupRole, LoginRole and WorkerRole when missing and sets the
// login roles' passwords. It is idempotent and safe to run before every "up".
func Roles(ctx context.Context, db *sql.DB, pw Passwords) error {
	if pw.API == "" {
		return errors.New("migrate: APP_DB_PASSWORD is required to create the API login role")
	}
	if pw.Worker == "" {
		return errors.New("migrate: WORKER_DB_PASSWORD is required to create the worker login role")
	}
	var err error
	for attempt := 1; attempt <= rolesAttempts; attempt++ {
		if err = rolesOnce(ctx, db, pw); err == nil || !concurrentRoleChange(err) {
			return err
		}
		delay := time.Duration(20+rand.IntN(60)) * time.Millisecond * time.Duration(attempt)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// rolesOnce runs the role changes in one transaction under an advisory lock.
// Role DDL is transactional in Postgres.
func rolesOnce(ctx context.Context, db *sql.DB, pw Passwords) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", rolesLockKey); err != nil {
		return fmt.Errorf("migrate: lock roles: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '`+GroupRole+`') THEN
    CREATE ROLE `+GroupRole+` NOLOGIN NOBYPASSRLS;
  END IF;
END
$$`); err != nil {
		return fmt.Errorf("migrate: create %s: %w", GroupRole, err)
	}

	if err = loginRole(ctx, tx, LoginRole, pw.API, true); err != nil {
		return err
	}
	// Not a member of GroupRole: 00011 revokes the membership 00009 gave and
	// grants the worker only what its jobs use. Roles does not revoke it
	// itself, because roles are cluster-wide and a database still before
	// 00011 relies on it.
	if err = loginRole(ctx, tx, WorkerRole, pw.Worker, false); err != nil {
		return err
	}
	// A schema owner that is not a superuser must be a member of WorkerRole to
	// hand River's objects to it and to keep migrating them afterwards.
	var super bool
	if err = tx.QueryRowContext(ctx, "SELECT rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&super); err != nil {
		return fmt.Errorf("migrate: check owner: %w", err)
	}
	if !super {
		if _, err = tx.ExecContext(ctx, "GRANT "+WorkerRole+" TO CURRENT_USER"); err != nil {
			return fmt.Errorf("migrate: grant %s to the owner: %w", WorkerRole, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit roles: %w", err)
	}
	return nil
}

// loginRole creates or updates a locked-down login role with password and,
// when inGroup is set, makes it a member of GroupRole.
func loginRole(ctx context.Context, tx *sql.Tx, role, password string, inGroup bool) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return fmt.Errorf("migrate: check %s: %w", role, err)
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	stmt := verb + " ROLE " + role + " WITH LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD " + quoteLiteral(password)
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("migrate: %s role: %w", role, err)
	}
	if !inGroup {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "GRANT "+GroupRole+" TO "+role); err != nil {
		return fmt.Errorf("migrate: grant %s to %s: %w", GroupRole, role, err)
	}
	return nil
}

// concurrentRoleChange reports whether err is Postgres refusing a role change
// because another session changed the same role at the same moment.
func concurrentRoleChange(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "XX000": // internal_error: "tuple concurrently updated"
		return strings.Contains(pgErr.Message, "concurrently")
	case "42710": // duplicate_object: "role already exists" from a racing CREATE
		return true
	}
	return false
}

// Up applies every pending goose migration, then River's job queue schema,
// and hands River's objects to WorkerRole with insert-only grants for
// LoginRole.
func Up(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return riverUp(ctx, db)
}

// Down rolls back the most recent goose migration. River's tables are left
// alone until the goose schema is gone entirely.
func Down(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	if _, err := p.Down(ctx); err != nil {
		return fmt.Errorf("migrate: down: %w", err)
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("migrate: version: %w", err)
	}
	if v == 0 {
		return riverDown(ctx, db)
	}
	return nil
}

// riverUp applies River's migrations, then calls miniloyal_secure_river()
// from migration 00009: WorkerRole owns every River object and LoginRole may
// only insert jobs. Both steps are idempotent, and the second also covers
// objects a River upgrade adds and undoes the blanket sequence grants some
// goose migrations give GroupRole. River's tables carry no tenant data, so
// they have no row-level security.
func riverUp(ctx context.Context, db *sql.DB) error {
	m, err := rivermigrate.New(riverdatabasesql.New(db), nil)
	if err != nil {
		return fmt.Errorf("migrate: river: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate: river up: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SELECT miniloyal_secure_river()"); err != nil {
		return fmt.Errorf("migrate: river grants: %w", err)
	}
	return nil
}

func riverDown(ctx context.Context, db *sql.DB) error {
	m, err := rivermigrate.New(riverdatabasesql.New(db), nil)
	if err != nil {
		return fmt.Errorf("migrate: river: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{TargetVersion: -1}); err != nil {
		return fmt.Errorf("migrate: river down: %w", err)
	}
	return nil
}

// Status returns one line per migration with its applied state.
func Status(ctx context.Context, db *sql.DB) ([]string, error) {
	p, err := provider(db)
	if err != nil {
		return nil, err
	}
	results, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: status: %w", err)
	}
	lines := make([]string, 0, len(results))
	for _, r := range results {
		state := "pending"
		if r.State == goose.StateApplied {
			state = "applied " + r.AppliedAt.UTC().Format("2006-01-02 15:04:05")
		}
		lines = append(lines, fmt.Sprintf("%-10s %s", state, r.Source.Path))
	}
	return lines, nil
}

// Version returns the current schema version.
func Version(ctx context.Context, db *sql.DB) (int64, error) {
	p, err := provider(db)
	if err != nil {
		return 0, err
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrate: version: %w", err)
	}
	return v, nil
}

func provider(db *sql.DB) (*goose.Provider, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return nil, fmt.Errorf("migrate: provider: %w", err)
	}
	return p, nil
}

// quoteLiteral renders s as a Postgres string literal.
func quoteLiteral(s string) string {
	if strings.ContainsRune(s, '\\') {
		return "E'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
