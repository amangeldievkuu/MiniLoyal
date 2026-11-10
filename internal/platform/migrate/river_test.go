package migrate_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// After Up, River's tables exist and belong to the worker role, never to the
// API or group role. Rolling migration 00009 back restores the Phase 3 shape
// (owner-owned tables, group grants), and applying it again to a database
// whose River tables already exist secures them again. Running Up twice is a
// no-op. db/migrations/worker_role_test.go covers the privileges themselves.
func TestRiverSchemaBelongsToTheWorker(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	owner, err := sql.Open("pgx", d.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	type state struct {
		workerOwned int
		groupUpdate bool
		apiInsert   bool
	}
	read := func() state {
		t.Helper()
		var s state
		if err := d.Admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM pg_tables WHERE schemaname = 'public'
			   AND tablename IN ('river_job', 'river_leader', 'river_queue', 'river_migration') AND tableowner = $1),
			has_table_privilege($2, 'river_job', 'UPDATE'),
			has_table_privilege($3, 'river_job', 'INSERT')`,
			migrate.WorkerRole, migrate.GroupRole, migrate.LoginRole).Scan(&s.workerOwned, &s.groupUpdate, &s.apiInsert); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// Since 00010 the API role has no River grants at all.
	secured := state{workerOwned: 4}
	if got := read(); got != secured {
		t.Fatalf("after Up: %+v, want %+v", got, secured)
	}

	// Rolling 00011 back makes the worker a member of miniloyal_app again,
	// cluster-wide, until Up revokes it; tests that assert what the worker
	// may not do wait while this runs.
	d.WhileChangingWorkerRole(t, func() {
		// Roll back to just before 00009, past any later migrations.
		for {
			v, err := migrate.Version(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			if v < 9 {
				break
			}
			if err := migrate.Down(ctx, owner); err != nil {
				t.Fatalf("down from %d: %v", v, err)
			}
		}
		if got, want := read(), (state{groupUpdate: true, apiInsert: true}); got != want {
			t.Fatalf("after Down: %+v, want the Phase 3 shape %+v", got, want)
		}

		for i := 1; i <= 2; i++ {
			if err := migrate.Up(ctx, owner); err != nil {
				t.Fatalf("Up %d: %v", i, err)
			}
			if got := read(); got != secured {
				t.Fatalf("after Up %d: %+v, want %+v", i, got, secured)
			}
		}
	})
	d.WithSteadyWorkerRole(t, func() {
		var inGroup bool
		if err := d.Admin.QueryRow(ctx, "SELECT pg_has_role($1, $2, 'MEMBER')", migrate.WorkerRole, migrate.GroupRole).Scan(&inGroup); err != nil {
			t.Fatal(err)
		}
		if inGroup {
			t.Errorf("after Up: %s is a member of %s", migrate.WorkerRole, migrate.GroupRole)
		}
	})
}
