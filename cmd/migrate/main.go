// Command migrate applies the embedded goose migrations in db/migrations.
//
//	migrate up            apply pending migrations (default)
//	migrate down          roll back the last migration
//	migrate status        list migrations and their state
//	migrate version       print the current schema version
//	migrate create NAME   create a new timestamped SQL migration file
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/amangeldievkuu/miniloyal/db/migrations"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "up"
	if len(args) > 0 {
		cmd = args[0]
	}
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if cmd == "create" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: migrate create NAME")
			return 2
		}
		goose.SetBaseFS(nil)
		name := strings.ReplaceAll(strings.ToLower(strings.Join(args[1:], "_")), "-", "_")
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
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate: open:", err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	switch cmd {
	case "up":
		err = goose.UpContext(ctx, db, ".")
	case "down":
		err = goose.DownContext(ctx, db, ".")
	case "status":
		err = goose.StatusContext(ctx, db, ".")
	case "version":
		err = goose.VersionContext(ctx, db, ".")
	default:
		fmt.Fprintf(os.Stderr, "migrate: unknown command %q\n", cmd)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	return 0
}
