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

// Role names. GroupRole owns no tables and carries the grants; LoginRole is
// what the API and worker connect as, as a member of GroupRole, so row-level
// security applies to them.
const (
	GroupRole = "miniloyal_app"
	LoginRole = "miniloyal_api"
)

// rolesLockKey serializes concurrent Roles calls within one database.
// Roles are cluster-wide, so callers connected to different databases can
// still collide; Postgres then fails with "tuple concurrently updated" or
// "role already exists", and Roles retries.
const (
	rolesLockKey  = 0x4d4c524f // "MLRO"
	rolesAttempts = 8
)

// Roles creates GroupRole and LoginRole when missing and sets LoginRole's
// password. It is idempotent and safe to run before every "up".
func Roles(ctx context.Context, db *sql.DB, loginPassword string) error {
	if loginPassword == "" {
		return errors.New("migrate: APP_DB_PASSWORD is required to create the login role")
	}
	var err error
	for attempt := 1; attempt <= rolesAttempts; attempt++ {
		if err = rolesOnce(ctx, db, loginPassword); err == nil || !concurrentRoleChange(err) {
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
func rolesOnce(ctx context.Context, db *sql.DB, loginPassword string) (err error) {
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

	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, LoginRole).Scan(&exists); err != nil {
		return fmt.Errorf("migrate: check %s: %w", LoginRole, err)
	}
	pw := quoteLiteral(loginPassword)
	var stmt string
	if exists {
		stmt = "ALTER ROLE " + LoginRole + " WITH LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD " + pw
	} else {
		stmt = "CREATE ROLE " + LoginRole + " WITH LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD " + pw
	}
	if _, err = tx.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("migrate: %s role: %w", LoginRole, err)
	}
	if _, err = tx.ExecContext(ctx, "GRANT "+GroupRole+" TO "+LoginRole); err != nil {
		return fmt.Errorf("migrate: grant %s: %w", GroupRole, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit roles: %w", err)
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

// Up applies every pending goose migration, then River's job queue schema
// (owned by the schema owner, like everything else) and the grants the
// application role needs to insert and work jobs.
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

// riverUp applies River's migrations and grants the application role access
// to River's tables, sequences and functions. Both steps are idempotent.
// River's tables carry no tenant data, so they have no row-level security.
func riverUp(ctx context.Context, db *sql.DB) error {
	m, err := rivermigrate.New(riverdatabasesql.New(db), nil)
	if err != nil {
		return fmt.Errorf("migrate: river: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate: river up: %w", err)
	}
	if _, err := db.ExecContext(ctx, riverGrants); err != nil {
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

// riverGrants gives GroupRole what a River client needs on every River
// object in the public schema.
const riverGrants = `DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT c.relname, c.relkind FROM pg_class c
           WHERE c.relnamespace = 'public'::regnamespace AND c.relname LIKE 'river\_%' AND c.relkind IN ('r', 'S')
  LOOP
    IF r.relkind = 'r' THEN
      EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO ` + GroupRole + `', r.relname);
    ELSE
      EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %I TO ` + GroupRole + `', r.relname);
    END IF;
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p
           WHERE p.pronamespace = 'public'::regnamespace AND p.proname LIKE 'river\_%'
  LOOP
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO ` + GroupRole + `', r.sig);
  END LOOP;
END
$$`

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
