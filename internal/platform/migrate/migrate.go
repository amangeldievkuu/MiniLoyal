// Package migrate applies the embedded goose migrations and creates the
// database roles the application connects with. The migrate binary and the
// test utilities both use it.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pressly/goose/v3"

	"github.com/amangeldievkuu/miniloyal/db/migrations"
)

// Role names. GroupRole owns no tables and carries the grants; LoginRole is
// what the API and worker connect as, as a member of GroupRole, so row-level
// security applies to them.
const (
	GroupRole = "miniloyal_app"
	LoginRole = "miniloyal_api"
)

// Roles creates GroupRole and LoginRole when missing and sets LoginRole's
// password. It is idempotent and safe to run before every "up".
func Roles(ctx context.Context, db *sql.DB, loginPassword string) error {
	if loginPassword == "" {
		return errors.New("migrate: APP_DB_PASSWORD is required to create the login role")
	}
	if _, err := db.ExecContext(ctx, `DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '`+GroupRole+`') THEN
    CREATE ROLE `+GroupRole+` NOLOGIN NOBYPASSRLS;
  END IF;
END
$$`); err != nil {
		return fmt.Errorf("migrate: create %s: %w", GroupRole, err)
	}

	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, LoginRole).Scan(&exists); err != nil {
		return fmt.Errorf("migrate: check %s: %w", LoginRole, err)
	}
	pw := quoteLiteral(loginPassword)
	var stmt string
	if exists {
		stmt = "ALTER ROLE " + LoginRole + " WITH LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD " + pw
	} else {
		stmt = "CREATE ROLE " + LoginRole + " WITH LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD " + pw
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("migrate: %s role: %w", LoginRole, err)
	}
	if _, err := db.ExecContext(ctx, "GRANT "+GroupRole+" TO "+LoginRole); err != nil {
		return fmt.Errorf("migrate: grant %s: %w", GroupRole, err)
	}
	return nil
}

// Up applies every pending migration.
func Up(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}

// Down rolls back the most recent migration.
func Down(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	if _, err := p.Down(ctx); err != nil {
		return fmt.Errorf("migrate: down: %w", err)
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
