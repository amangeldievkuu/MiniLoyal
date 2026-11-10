package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/jobs"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// riverTables are River's tables in River v0.51.
var riverTables = []string{"river_job", "river_leader", "river_migration", "river_notification", "river_queue"}

// Security review Phase 3, accepted finding closed in Phase 4 (00009): the
// worker role owns River's tables and works jobs; it cannot bypass row-level
// security and owns nothing else.
func TestWorkerRoleOwnsRiverOnly(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	var login, super, bypass, inApp bool
	if err := d.Admin.QueryRow(ctx, `SELECT rolcanlogin, rolsuper, rolbypassrls, pg_has_role(rolname, $2, 'USAGE')
		FROM pg_roles WHERE rolname = $1`, migrate.WorkerRole, migrate.GroupRole).Scan(&login, &super, &bypass, &inApp); err != nil {
		t.Fatal(err)
	}
	if !login || super || bypass || !inApp {
		t.Fatalf("%s: login %v, superuser %v, bypassrls %v, member of %s %v", migrate.WorkerRole, login, super, bypass, migrate.GroupRole, inApp)
	}

	rows, err := d.Admin.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
		WHERE c.relnamespace = 'public'::regnamespace AND r.rolname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'S')`, migrate.WorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	owned := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		owned[name] = true
		if !strings.HasPrefix(name, "river_") {
			t.Errorf("%s owns %s, want River objects only", migrate.WorkerRole, name)
		}
	}
	rows.Close()
	for _, table := range riverTables {
		if !owned[table] {
			t.Errorf("%s does not own %s", migrate.WorkerRole, table)
		}
	}
	if err := jobs.CheckWorkerRole(ctx, d.Worker); err != nil {
		t.Errorf("worker pool: %v", err)
	}
}

// Since 00010 the API role has nothing on River: cmd/api enqueues no jobs,
// so it cannot insert, read, rewrite, cancel or delete one, and cannot
// touch River's other tables. The worker inserts and works jobs.
func TestAPIRoleHasNoRiverAccess(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	apiInserter, err := jobs.NewInserter(d.App)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apiInserter.Insert(ctx, jobs.MaintenanceArgs{}, nil); db.Code(err) != db.CodeInsufficientPrivilege {
		t.Fatalf("insert as the API role: %v, want permission denied", err)
	}
	inserter, err := jobs.NewInserter(d.Worker)
	if err != nil {
		t.Fatal(err)
	}
	first, err := inserter.Insert(ctx, jobs.MaintenanceArgs{}, nil)
	if err != nil {
		t.Fatalf("unique insert as the worker role: %v", err)
	}
	second, err := inserter.Insert(ctx, jobs.MaintenanceArgs{}, nil)
	if err != nil {
		t.Fatalf("duplicate unique insert as the worker role: %v", err)
	}
	if !second.UniqueSkippedAsDuplicate || second.Job.ID != first.Job.ID {
		t.Fatalf("duplicate insert: skipped %v, id %d, want the first job %d", second.UniqueSkippedAsDuplicate, second.Job.ID, first.Job.ID)
	}
	id := first.Job.ID

	denied := map[string][]any{
		"SELECT count(*) FROM river_job": nil,
		"INSERT INTO river_job (args, kind, max_attempts, priority, queue, state) VALUES ('{}', 'maintenance', 1, 1, 'default', 'available')": nil,
		"UPDATE river_job SET state = 'cancelled', finalized_at = now() WHERE id = $1":                                                        {id},
		"UPDATE river_job SET kind = 'points_expiry' WHERE id = $1":                                                                           {id},
		"DELETE FROM river_job WHERE id = $1":                                                                                                 {id},
		"TRUNCATE river_job":                                                                                                                  nil,
		"SELECT nextval('river_job_id_seq')":                                                                                                  nil,
	}
	for _, table := range riverTables[1:] {
		denied["SELECT count(*) FROM "+table] = nil
		denied["DELETE FROM "+table] = nil
	}
	denied["INSERT INTO river_leader (leader_id, elected_at, expires_at) VALUES ('evil', now(), now() + interval '1 hour')"] = nil
	denied["UPDATE river_queue SET paused_at = now()"] = nil
	for stmt, args := range denied {
		if _, err := d.App.Exec(ctx, stmt, args...); db.Code(err) != db.CodeInsufficientPrivilege {
			t.Errorf("API role ran %q: %v, want permission denied", stmt, err)
		}
	}

	for _, table := range riverTables {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			var group, api bool
			if err := d.Admin.QueryRow(ctx, "SELECT has_table_privilege($1, $3, $4), has_table_privilege($2, $3, $4)",
				migrate.GroupRole, migrate.LoginRole, table, priv).Scan(&group, &api); err != nil {
				t.Fatal(err)
			}
			if group || api {
				t.Errorf("%s on %s: %s %v, %s %v, want neither", priv, table, migrate.GroupRole, group, migrate.LoginRole, api)
			}
		}
	}
	var kindUpdate bool
	if err := d.Admin.QueryRow(ctx, "SELECT has_column_privilege($1, 'river_job', 'kind', 'UPDATE')", migrate.LoginRole).Scan(&kindUpdate); err != nil {
		t.Fatal(err)
	}
	if kindUpdate {
		t.Error("the API role kept UPDATE (kind) on river_job")
	}

	// The worker changes and removes jobs.
	if _, err := d.Worker.Exec(ctx, "UPDATE river_job SET state = 'cancelled', finalized_at = now() WHERE id = $1", id); err != nil {
		t.Fatalf("worker update: %v", err)
	}
	if _, err := d.Worker.Exec(ctx, "DELETE FROM river_job WHERE id = $1", id); err != nil {
		t.Fatalf("worker delete: %v", err)
	}
	if err := jobs.CheckWorkerRole(ctx, d.App); err == nil {
		t.Error("the API role passed the worker role check")
	}
}
