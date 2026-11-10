// Package jobs runs MiniLoyal's background work on River, a job queue that
// lives in PostgreSQL: periodic maintenance, points expiry and the daily
// reconciliation of points, stamps and coupons. River's leader election makes one worker process
// the scheduler, and each periodic job is unique per period, so running
// several workers never double-runs a job.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/coupon"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/webhook"
)

// Schedules.
const (
	MaintenanceInterval = time.Hour
	ExpiryInterval      = 15 * time.Minute
	// ExpiryBatch is how many lots one ExpireDue call handles; the job keeps
	// calling until a call returns fewer.
	ExpiryBatch = 500
	// reconcileHour is the local hour (Asia/Taipei) the reconciliation runs.
	reconcileHour = 3
)

// Expirer expires points lots whose expiry has passed. internal/points
// implements it.
type Expirer interface {
	ExpireDue(ctx context.Context, now time.Time, batch int) (int, error)
}

// Reconciler checks cached counters against their append-only history.
// internal/points implements it for points accounts, internal/stamp for
// stamp progress and coupon template counts.
type Reconciler interface {
	Reconcile(ctx context.Context, now time.Time) (checked int, mismatches int, err error)
}

// Deps are what the workers need. A nil Expirer leaves that job
// unregistered; the daily reconciliation runs when Reconciler or Loyalty
// is set and runs each one that is.
type Deps struct {
	Pool       *pgxpool.Pool
	Expirer    Expirer
	Reconciler Reconciler
	// Loyalty reconciles stamp progress and coupon templates.
	Loyalty Reconciler
	Clock   clock.Clock
	Log     *slog.Logger
}

func (d *Deps) reconciles() bool { return d.Reconciler != nil || d.Loyalty != nil }

func (d *Deps) defaults() {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
}

// ------------------------------------------------------------------------- maintenance

// Job arguments are stored in River's tables, which the API role can read
// (River's insert returns the row) and which carry no row-level security. Arguments must therefore hold
// IDs only: never personal data, message text, tokens or secrets. A job loads
// what it needs inside a tenant transaction when it runs.

// MaintenanceArgs deletes spent rows that keep the identity and webhook
// tables bounded: expired idempotency outcomes, spent refresh tokens, old MFA
// challenges, old inbound webhook dedupe records and old scan use records
// (points and coupons).
type MaintenanceArgs struct{}

// Kind implements river.JobArgs.
func (MaintenanceArgs) Kind() string { return "maintenance" }

// InsertOpts makes the job unique per run period.
func (MaintenanceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: MaintenanceInterval}}
}

// MaintenanceWorker runs MaintenanceArgs.
type MaintenanceWorker struct {
	river.WorkerDefaults[MaintenanceArgs]
	deps Deps
}

// Work runs every cleanup; one failing does not stop the others, and the
// joined error makes River retry the job.
func (w *MaintenanceWorker) Work(ctx context.Context, _ *river.Job[MaintenanceArgs]) error {
	pool := w.deps.Pool
	var errs []error
	idem, err := idempotency.New(pool, nil, w.deps.Clock).DeleteExpired(ctx)
	errs = append(errs, err)
	tokens, err := auth.SweepSpentRefreshTokens(ctx, pool)
	errs = append(errs, err)
	challenges, err := auth.DeleteExpiredMFAChallenges(ctx, pool)
	errs = append(errs, err)
	events, err := webhook.DeleteOldInboundEvents(ctx, pool)
	errs = append(errs, err)
	scans, err := points.DeleteOldScanTokenUses(ctx, pool, w.deps.Clock.Now())
	errs = append(errs, err)
	couponScans, err := coupon.DeleteOldScanRedeems(ctx, pool, w.deps.Clock.Now())
	errs = append(errs, err)
	if err := errors.Join(errs...); err != nil {
		w.deps.Log.ErrorContext(ctx, "maintenance failed", "err", err)
		return err
	}
	w.deps.Log.InfoContext(ctx, "maintenance run", "idempotency_keys", idem, "refresh_tokens", tokens,
		"mfa_challenges", challenges, "webhook_events", events, "scan_token_uses", scans, "scan_coupon_redeems", couponScans)
	return nil
}

// ------------------------------------------------------------------------- points expiry

// PointsExpiryArgs expires points lots whose expiry has passed.
type PointsExpiryArgs struct{}

// Kind implements river.JobArgs.
func (PointsExpiryArgs) Kind() string { return "points_expiry" }

// InsertOpts makes the job unique per run period.
func (PointsExpiryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: ExpiryInterval}}
}

// PointsExpiryWorker runs PointsExpiryArgs.
type PointsExpiryWorker struct {
	river.WorkerDefaults[PointsExpiryArgs]
	deps Deps
}

// Work expires in batches until a batch comes back short or the job's
// context ends. Every call uses the same "now" so a slow run does not chase
// lots that expire while it works.
func (w *PointsExpiryWorker) Work(ctx context.Context, _ *river.Job[PointsExpiryArgs]) error {
	now := w.deps.Clock.Now()
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := w.deps.Expirer.ExpireDue(ctx, now, ExpiryBatch)
		total += n
		if err != nil {
			w.deps.Log.ErrorContext(ctx, "points expiry failed", "err", err, "expired", total)
			return fmt.Errorf("points expiry: %w", err)
		}
		if n < ExpiryBatch {
			break
		}
	}
	w.deps.Log.InfoContext(ctx, "points expiry run", "expired_lots", total)
	return nil
}

// --------------------------------------------------------------------- reconciliation

// PointsReconcileArgs runs the daily reconciliation: every points account
// against its ledger and open lots, every stamp progress row against its
// events and every coupon template's claimed_count against its coupons.
// The kind keeps its Phase 3 name so scheduled jobs stay unique.
type PointsReconcileArgs struct{}

// Kind implements river.JobArgs.
func (PointsReconcileArgs) Kind() string { return "points_reconcile" }

// InsertOpts makes the job unique per day.
func (PointsReconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: 24 * time.Hour}}
}

// PointsReconcileWorker runs PointsReconcileArgs.
type PointsReconcileWorker struct {
	river.WorkerDefaults[PointsReconcileArgs]
	deps Deps
}

// Work runs each check; one failing does not stop the other, and the
// joined error makes River retry the job. Mismatches are an ERROR log line:
// that is the alert.
func (w *PointsReconcileWorker) Work(ctx context.Context, _ *river.Job[PointsReconcileArgs]) error {
	now := w.deps.Clock.Now()
	var errs []error
	for _, c := range []struct {
		name string
		r    Reconciler
	}{{"points", w.deps.Reconciler}, {"loyalty", w.deps.Loyalty}} {
		if c.r == nil {
			continue
		}
		checked, mismatches, err := c.r.Reconcile(ctx, now)
		switch {
		case err != nil:
			w.deps.Log.ErrorContext(ctx, c.name+" reconciliation failed", "err", err)
			errs = append(errs, fmt.Errorf("%s reconciliation: %w", c.name, err))
		case mismatches > 0:
			w.deps.Log.ErrorContext(ctx, c.name+" reconciliation found mismatches", "checked", checked, "mismatches", mismatches)
		default:
			w.deps.Log.InfoContext(ctx, c.name+" reconciliation clean", "checked", checked)
		}
	}
	return errors.Join(errs...)
}

// --------------------------------------------------------------------------- schedules

// DailyAt fires once a day at hour:00 in loc.
type DailyAt struct {
	Hour int
	Loc  *time.Location
}

// Next implements river.PeriodicSchedule.
func (d DailyAt) Next(current time.Time) time.Time {
	t := current.In(d.Loc)
	next := time.Date(t.Year(), t.Month(), t.Day(), d.Hour, 0, 0, 0, d.Loc)
	if !next.After(t) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// ReconcileSchedule is 03:00 every day in Taipei.
func ReconcileSchedule() DailyAt {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		// Taiwan has no daylight saving time, so a fixed zone is exact.
		loc = time.FixedZone("Asia/Taipei", 8*60*60)
	}
	return DailyAt{Hour: reconcileHour, Loc: loc}
}

// --------------------------------------------------------------------------- clients

// Workers registers every worker whose dependencies are present.
func Workers(deps Deps) *river.Workers {
	deps.defaults()
	w := river.NewWorkers()
	river.AddWorker(w, &MaintenanceWorker{deps: deps})
	if deps.Expirer != nil {
		river.AddWorker(w, &PointsExpiryWorker{deps: deps})
	} else {
		deps.Log.Warn("points expiry job disabled: no expirer configured")
	}
	if deps.reconciles() {
		river.AddWorker(w, &PointsReconcileWorker{deps: deps})
	} else {
		deps.Log.Warn("reconciliation job disabled: no reconciler configured")
	}
	return w
}

// PeriodicJobs returns the schedules for the registered workers.
func PeriodicJobs(deps Deps) []*river.PeriodicJob {
	jobs := []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(MaintenanceInterval),
			func() (river.JobArgs, *river.InsertOpts) { return MaintenanceArgs{}, nil },
			&river.PeriodicJobOpts{ID: "maintenance", RunOnStart: true}),
	}
	if deps.Expirer != nil {
		jobs = append(jobs, river.NewPeriodicJob(river.PeriodicInterval(ExpiryInterval),
			func() (river.JobArgs, *river.InsertOpts) { return PointsExpiryArgs{}, nil },
			&river.PeriodicJobOpts{ID: "points_expiry", RunOnStart: true}))
	}
	if deps.reconciles() {
		jobs = append(jobs, river.NewPeriodicJob(ReconcileSchedule(),
			func() (river.JobArgs, *river.InsertOpts) { return PointsReconcileArgs{}, nil },
			&river.PeriodicJobOpts{ID: "points_reconcile"}))
	}
	return jobs
}

// NewClient returns a River client that works the default queue with
// concurrency workers and schedules the periodic jobs.
func NewClient(pool *pgxpool.Pool, deps Deps, concurrency int) (*river.Client[pgx.Tx], error) {
	deps.defaults()
	deps.Pool = pool
	if concurrency < 1 {
		concurrency = 1
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: concurrency}},
		Workers:      Workers(deps),
		PeriodicJobs: PeriodicJobs(deps),
		Logger:       deps.Log,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: river client: %w", err)
	}
	return client, nil
}

// WorkerRole is the database role the worker connects as. It owns River's
// tables; the API role may only insert jobs (migration 00009).
const WorkerRole = "miniloyal_worker"

// ErrNotWorkerRole means the pool cannot work jobs safely: it is not
// connected as WorkerRole, or it is connected as a role that skips
// row-level security.
var ErrNotWorkerRole = errors.New("jobs: the worker must connect as " + WorkerRole + " (WORKER_DATABASE_URL)")

// CheckWorkerRole fails fast when pool is connected as any role but
// WorkerRole itself, so a worker pointed at the API's URL, or at another
// role that merely belongs to WorkerRole (the schema owner does), stops at
// start instead of failing every job, and a worker pointed at a superuser
// does not run without row-level security. Both the session user (who
// logged in) and the current user must be WorkerRole: a superuser session
// that ran SET ROLE, for example through the connection string's options,
// keeps its rights and can RESET ROLE at any time.
func CheckWorkerRole(ctx context.Context, pool *pgxpool.Pool) error {
	var user, session string
	var super, bypass bool
	if err := pool.QueryRow(ctx, `SELECT current_user, session_user,
		(SELECT bool_or(r.rolsuper) FROM pg_roles r WHERE r.rolname IN (current_user, session_user)),
		(SELECT bool_or(r.rolbypassrls) FROM pg_roles r WHERE r.rolname IN (current_user, session_user))`).
		Scan(&user, &session, &super, &bypass); err != nil {
		return fmt.Errorf("jobs: check role: %w", err)
	}
	if user != WorkerRole || session != WorkerRole || super || bypass {
		return fmt.Errorf("%w: connected as %s, session %s (superuser %v, bypassrls %v)", ErrNotWorkerRole, user, session, super, bypass)
	}
	return nil
}

// NewInserter returns an insert-only client for processes that enqueue jobs
// but do not work them. Its role needs only SELECT and INSERT on river_job,
// UPDATE of river_job.kind for unique inserts, and the river_job_id_seq
// sequence. The API role has none of these since migration 00010, because
// cmd/api enqueues nothing yet; the migration that makes it enqueue grants
// them again.
func NewInserter(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("jobs: river inserter: %w", err)
	}
	return client, nil
}
