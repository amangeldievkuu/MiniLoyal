package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

type fakeExpirer struct {
	mu      sync.Mutex
	returns []int
	err     error
	calls   int
	nows    []time.Time
}

func (f *fakeExpirer) ExpireDue(_ context.Context, now time.Time, batch int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.nows = append(f.nows, now)
	if batch != ExpiryBatch {
		return 0, errors.New("unexpected batch size")
	}
	if f.err != nil {
		return 0, f.err
	}
	if len(f.returns) == 0 {
		return 0, nil
	}
	n := f.returns[0]
	f.returns = f.returns[1:]
	return n, nil
}

type fakeReconciler struct {
	checked, mismatches int
	err                 error
}

func (f fakeReconciler) Reconcile(context.Context, time.Time) (int, int, error) {
	return f.checked, f.mismatches, f.err
}

func captureLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestExpiryWorkerLoopsUntilAShortBatch(t *testing.T) {
	fixed := clock.At(time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC))
	exp := &fakeExpirer{returns: []int{500, 500, 37}}
	log, buf := captureLog()
	w := &PointsExpiryWorker{deps: Deps{Expirer: exp, Clock: fixed, Log: log}}
	if err := w.Work(context.Background(), &river.Job[PointsExpiryArgs]{}); err != nil {
		t.Fatal(err)
	}
	if exp.calls != 3 {
		t.Fatalf("calls = %d, want 3", exp.calls)
	}
	for _, n := range exp.nows {
		if !n.Equal(fixed.Now()) {
			t.Errorf("every batch must use the same now: %s", n)
		}
	}
	if !strings.Contains(buf.String(), "expired_lots=1037") {
		t.Errorf("log: %s", buf)
	}
}

func TestExpiryWorkerReturnsErrorsForRetry(t *testing.T) {
	boom := errors.New("db down")
	log, _ := captureLog()
	w := &PointsExpiryWorker{deps: Deps{Expirer: &fakeExpirer{err: boom}, Clock: clock.Real{}, Log: log}}
	if err := w.Work(context.Background(), &river.Job[PointsExpiryArgs]{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the expirer's error", err)
	}
}

func TestExpiryWorkerStopsWhenCancelled(t *testing.T) {
	exp := &fakeExpirer{returns: []int{500, 500, 500, 500}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	log, _ := captureLog()
	w := &PointsExpiryWorker{deps: Deps{Expirer: exp, Clock: clock.Real{}, Log: log}}
	if err := w.Work(ctx, &river.Job[PointsExpiryArgs]{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if exp.calls != 0 {
		t.Errorf("calls after cancel = %d", exp.calls)
	}
}

func TestReconcileWorkerAlertsOnMismatches(t *testing.T) {
	log, buf := captureLog()
	w := &PointsReconcileWorker{deps: Deps{Reconciler: fakeReconciler{checked: 120, mismatches: 2}, Clock: clock.Real{}, Log: log}}
	if err := w.Work(context.Background(), &river.Job[PointsReconcileArgs]{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "mismatches=2") || !strings.Contains(out, "accounts_checked=120") {
		t.Errorf("mismatches must log an ERROR with counts: %s", out)
	}

	log, buf = captureLog()
	w = &PointsReconcileWorker{deps: Deps{Reconciler: fakeReconciler{checked: 120}, Clock: clock.Real{}, Log: log}}
	if err := w.Work(context.Background(), &river.Job[PointsReconcileArgs]{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "level=INFO") {
		t.Errorf("a clean run logs INFO only: %s", buf)
	}

	boom := errors.New("query failed")
	w = &PointsReconcileWorker{deps: Deps{Reconciler: fakeReconciler{err: boom}, Clock: clock.Real{}, Log: log}}
	if err := w.Work(context.Background(), &river.Job[PointsReconcileArgs]{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestReconcileScheduleIsThreeInTheMorningInTaipei(t *testing.T) {
	s := ReconcileSchedule()
	taipei := time.FixedZone("TPE", 8*3600)
	cases := []struct{ now, want time.Time }{
		// 01:00 Taipei: later the same day.
		{time.Date(2026, 11, 10, 1, 0, 0, 0, taipei), time.Date(2026, 11, 10, 3, 0, 0, 0, taipei)},
		// Exactly 03:00: the next day, never the same instant again.
		{time.Date(2026, 11, 10, 3, 0, 0, 0, taipei), time.Date(2026, 11, 11, 3, 0, 0, 0, taipei)},
		// 23:00 UTC is 07:00 Taipei the next day: 03:00 the day after.
		{time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 3, 0, 0, 0, taipei)},
		// 18:59 UTC is 02:59 Taipei: one minute later.
		{time.Date(2026, 12, 31, 18, 59, 0, 0, time.UTC), time.Date(2027, 1, 1, 3, 0, 0, 0, taipei)},
	}
	for _, tc := range cases {
		if got := s.Next(tc.now); !got.Equal(tc.want) {
			t.Errorf("Next(%s) = %s, want %s", tc.now, got, tc.want)
		}
	}
}

func TestOptionalJobsAreSkippedWithAWarning(t *testing.T) {
	log, buf := captureLog()
	deps := Deps{Log: log}
	Workers(deps)
	if got := len(PeriodicJobs(deps)); got != 1 {
		t.Errorf("periodic jobs without points = %d, want 1 (maintenance)", got)
	}
	if strings.Count(buf.String(), "level=WARN") != 2 {
		t.Errorf("want two WARN lines: %s", buf)
	}
	full := Deps{Log: log, Expirer: &fakeExpirer{}, Reconciler: fakeReconciler{}}
	if got := len(PeriodicJobs(full)); got != 3 {
		t.Errorf("periodic jobs with points = %d, want 3", got)
	}
}

func TestPeriodicArgsAreUniquePerPeriod(t *testing.T) {
	for _, tc := range []struct {
		opts river.InsertOpts
		want time.Duration
	}{
		{MaintenanceArgs{}.InsertOpts(), MaintenanceInterval},
		{PointsExpiryArgs{}.InsertOpts(), ExpiryInterval},
		{PointsReconcileArgs{}.InsertOpts(), 24 * time.Hour},
	} {
		if tc.opts.UniqueOpts.ByPeriod != tc.want {
			t.Errorf("ByPeriod = %s, want %s", tc.opts.UniqueOpts.ByPeriod, tc.want)
		}
	}
}

// End to end on a real database with the production roles: the API role's
// inserter enqueues, a worker client connected as the worker role works the
// job, and the cleanups really run.
func TestMaintenanceRunsThroughRiver(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	// One expired idempotency outcome and one old inbound webhook record.
	tenant := d.NewTenant(t, "Jobs")
	app := testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'App', 'app')", app, tenant)
	d.Exec(t, "INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, expires_at) VALUES ($1, 'user:x', 'k', '\\x00', now() - interval '1 hour')", tenant)
	d.Exec(t, "INSERT INTO webhook_inbound_events (tenant_id, app_id, line_webhook_event_id, event_type, received_at) VALUES ($1, $2, 'old', 'follow', now() - interval '8 days')", tenant, app)

	log, buf := captureLog()
	client, err := NewClient(d.Worker, Deps{Log: log}, 2)
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

	inserter, err := NewInserter(d.App)
	if err != nil {
		t.Fatal(err)
	}
	// The periodic job runs on start too; insert one more explicitly without
	// the per-period uniqueness so the inserter path is exercised.
	res, err := inserter.Insert(ctx, MaintenanceArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{}})
	if err != nil {
		t.Fatalf("insert as the API role: %v", err)
	}

	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev := <-done:
			if ev.Job.ID != res.Job.ID {
				continue
			}
			if ev.Kind != river.EventKindJobCompleted || ev.Job.State != rivertype.JobStateCompleted {
				t.Fatalf("job ended %s: %v\nlog: %s", ev.Kind, ev.Job.Errors, buf)
			}
			var idem, events int
			if err := d.Admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM idempotency_keys), (SELECT count(*) FROM webhook_inbound_events)").Scan(&idem, &events); err != nil {
				t.Fatal(err)
			}
			if idem != 0 || events != 0 {
				t.Errorf("cleanups did not run: idempotency_keys=%d webhook_inbound_events=%d", idem, events)
			}
			return
		case <-deadline:
			t.Fatalf("job did not complete\nlog: %s", buf)
		}
	}
}

// The worker refuses the API's role, which cannot run jobs, and the owner
// role, which would run them without row-level security.
func TestCheckWorkerRole(t *testing.T) {
	if WorkerRole != migrate.WorkerRole {
		t.Fatalf("WorkerRole %q differs from migrate.WorkerRole %q", WorkerRole, migrate.WorkerRole)
	}
	d := testutil.NewDB(t)
	ctx := context.Background()
	if err := CheckWorkerRole(ctx, d.Worker); err != nil {
		t.Errorf("worker role rejected: %v", err)
	}
	for name, pool := range map[string]*pgxpool.Pool{"api": d.App, "owner": d.Admin} {
		if err := CheckWorkerRole(ctx, pool); !errors.Is(err, ErrNotWorkerRole) {
			t.Errorf("%s role: %v, want ErrNotWorkerRole", name, err)
		}
	}
}
