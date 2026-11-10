package jobs

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// fixedRiverTime pins River's notion of now, so both inserts below fall in
// one uniqueness period whatever the wall clock says.
type fixedRiverTime struct{ t time.Time }

func (f fixedRiverTime) Now() time.Time       { return f.t }
func (f fixedRiverTime) NowOrNil() *time.Time { return &f.t }

// countingExpirer counts the runs that reach the points service.
type countingExpirer struct {
	next  Expirer
	calls atomic.Int64
}

func (c *countingExpirer) ExpireDue(ctx context.Context, now time.Time, batch int) (int, error) {
	c.calls.Add(1)
	return c.next.ExpireDue(ctx, now, batch)
}

// Invariant: the worker connects as the application role, which is neither
// a superuser, nor exempt from row-level security, nor the owner of the
// points tables, and the real points expiry runs through River under it.
// A points expiry job inserted twice in one period is stored once and runs
// once.
func TestPointsExpiryJobRunsOnceAsTheApplicationRole(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	var user, owner string
	var super, bypass bool
	if err := d.App.QueryRow(ctx, `SELECT current_user, r.rolsuper, r.rolbypassrls,
		(SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = 'points_lots')
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&user, &super, &bypass, &owner); err != nil {
		t.Fatal(err)
	}
	if super || bypass || user == owner {
		t.Fatalf("worker role %s: superuser %v, bypassrls %v, owner of points_lots %s", user, super, bypass, owner)
	}

	// One member with a lot that expired yesterday.
	tenantID := d.NewTenant(t, "Jobs")
	app, member := testutil.NewID(), testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'App', 'app')", app, tenantID)
	d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, 'Ujobs')", member, tenantID, app)
	d.Exec(t, "INSERT INTO points_accounts (member_id, tenant_id, app_id, balance, lifetime_earned) VALUES ($1, $2, $3, 10, 10)", member, tenantID, app)
	d.Exec(t, `WITH lot AS (
		INSERT INTO points_lots (tenant_id, app_id, member_id, earned, remaining, expires_at)
		VALUES ($1, $2, $3, 10, 10, now() - interval '1 day') RETURNING id)
		INSERT INTO points_ledger (tenant_id, app_id, member_id, txn_id, lot_id, delta, balance_after, reason, actor_type)
		SELECT $1, $2, $3, gen_random_uuid(), id, 10, 10, 'earn', 'system' FROM lot`, tenantID, app, member)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := points.New(points.Deps{Pool: d.App, Tenants: tenant.New(d.App, crypto.Static(bytes.Repeat([]byte{5}, 32)), clock.Real{}, log), Clock: clock.Real{}, Log: log})
	exp := &countingExpirer{next: svc}

	// Two inserts in one period, as two worker processes scheduling at once.
	pinned := time.Now().Add(-24 * time.Hour).Truncate(ExpiryInterval).Add(ExpiryInterval / 2)
	inserter, err := river.NewClient(riverpgxv5.New(d.App), &river.Config{Test: river.TestConfig{Time: fixedRiverTime{pinned}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := inserter.Insert(ctx, PointsExpiryArgs{}, nil)
	if err != nil {
		t.Fatalf("insert as the application role: %v", err)
	}
	second, err := inserter.Insert(ctx, PointsExpiryArgs{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.UniqueSkippedAsDuplicate || !second.UniqueSkippedAsDuplicate || second.Job.ID != first.Job.ID {
		t.Fatalf("inserts: first skipped=%v id=%d, second skipped=%v id=%d", first.UniqueSkippedAsDuplicate, first.Job.ID, second.UniqueSkippedAsDuplicate, second.Job.ID)
	}

	// A worker without periodic jobs, so the only expiry job is ours.
	deps := Deps{Pool: d.App, Expirer: exp, Clock: clock.Real{}, Log: log}
	client, err := river.NewClient(riverpgxv5.New(d.App), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		Workers: Workers(deps),
		Logger:  log,
	})
	if err != nil {
		t.Fatal(err)
	}
	done, cancelSub := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
	defer cancelSub()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.StopAndCancel(stopCtx)
	})

	deadline := time.After(30 * time.Second)
wait:
	for {
		select {
		case ev := <-done:
			if ev.Job.ID != first.Job.ID {
				continue
			}
			if ev.Kind != river.EventKindJobCompleted || ev.Job.State != rivertype.JobStateCompleted {
				t.Fatalf("job ended %s: %v", ev.Kind, ev.Job.Errors)
			}
			break wait
		case <-deadline:
			t.Fatal("expiry job did not complete")
		}
	}

	var jobs, remaining, expireRows int
	var balance int64
	if err := d.Admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM river_job WHERE kind = 'points_expiry'),
		(SELECT remaining FROM points_lots WHERE member_id = $1),
		(SELECT count(*) FROM points_ledger WHERE member_id = $1 AND reason = 'expire'),
		(SELECT balance FROM points_accounts WHERE member_id = $1)`, member).Scan(&jobs, &remaining, &expireRows, &balance); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || exp.calls.Load() != 1 {
		t.Fatalf("expiry jobs stored %d, runs %d; want one of each", jobs, exp.calls.Load())
	}
	if remaining != 0 || expireRows != 1 || balance != 0 {
		t.Fatalf("after the job: lot remaining %d, expire rows %d, balance %d", remaining, expireRows, balance)
	}
	if _, mismatches, err := svc.Reconcile(ctx, time.Now()); err != nil || mismatches != 0 {
		t.Fatalf("reconcile after the job: %d mismatches, %v", mismatches, err)
	}
}
