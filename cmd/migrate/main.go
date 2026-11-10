// Command migrate manages the database schema and the application roles.
//
//	migrate up            create roles, then apply pending migrations (default)
//	migrate down          roll back the last migration
//	migrate status        list migrations and their state
//	migrate version       print the current schema version
//	migrate roles         create or update the application roles only
//	migrate create NAME   create a new numbered SQL migration file
//
// It connects with MIGRATE_DATABASE_URL (the owner), falling back to
// DATABASE_URL, and sets the login roles' passwords from APP_DB_PASSWORD
// (miniloyal_api) and WORKER_DB_PASSWORD (miniloyal_worker).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "up"
	if len(args) > 0 {
		cmd = args[0]
	}

	if cmd == "create" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: migrate create NAME")
			return 2
		}
		name := strings.ReplaceAll(strings.ToLower(strings.Join(args[1:], "_")), "-", "_")
		goose.SetSequential(true)
		if err := goose.Create(nil, "db/migrations", name, "sql"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	db, err := sql.Open("pgx", cfg.MigrateDatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate: open:", err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	passwords := migrate.Passwords{API: cfg.AppDBPassword, Worker: cfg.WorkerDBPassword}
	switch cmd {
	case "up":
		if err = migrate.Roles(ctx, db, passwords); err == nil {
			err = migrate.Up(ctx, db)
		}
	case "roles":
		err = migrate.Roles(ctx, db, passwords)
	case "down":
		err = migrate.Down(ctx, db)
	case "status":
		var lines []string
		if lines, err = migrate.Status(ctx, db); err == nil {
			for _, l := range lines {
				fmt.Println(l)
			}
		}
	case "version":
		var v int64
		if v, err = migrate.Version(ctx, db); err == nil {
			fmt.Println(v)
		}
	default:
		fmt.Fprintf(os.Stderr, "migrate: unknown command %q\n", cmd)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
