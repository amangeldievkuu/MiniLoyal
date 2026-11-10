package migrate_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// After Up, River's tables exist, belong to the schema owner (never to the
// application roles) and the application role can insert, read, update and
// delete jobs. Running Up again is a no-op.
func TestRiverSchemaAndGrants(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	var tables int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_tables
		WHERE schemaname = 'public' AND tablename IN ('river_job', 'river_leader', 'river_queue', 'river_migration')
		  AND tableowner NOT IN ($1, $2)`, migrate.GroupRole, migrate.LoginRole).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 4 {
		t.Fatalf("river tables owned by the owner = %d, want 4", tables)
	}

	var id int64
	if err := d.App.QueryRow(ctx, `INSERT INTO river_job (args, kind, max_attempts, queue, state)
		VALUES ('{}', 'grant_probe', 1, 'default', 'available') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("application role cannot insert a job: %v", err)
	}
	if _, err := d.App.Exec(ctx, "UPDATE river_job SET state = 'cancelled', finalized_at = now() WHERE id = $1", id); err != nil {
		t.Fatalf("application role cannot update a job: %v", err)
	}
	if _, err := d.App.Exec(ctx, "DELETE FROM river_job WHERE id = $1", id); err != nil {
		t.Fatalf("application role cannot delete a job: %v", err)
	}

	owner, err := sql.Open("pgx", d.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := migrate.Up(ctx, owner); err != nil {
		t.Fatalf("second Up: %v", err)
	}
}
