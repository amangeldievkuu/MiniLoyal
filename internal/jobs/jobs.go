// Package jobs runs MiniLoyal's background work on River, a job queue that
// lives in PostgreSQL: periodic maintenance, points expiry and the daily
// points reconciliation. River's leader election makes one worker process
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

// Reconciler checks every points account against its ledger and lots.
// internal/points implements it.
type Reconciler interface {
	Reconcile(ctx context.Context, now time.Time) (checked int, mismatches int, err error)
}

// Deps are what the workers need. A nil Expirer or Reconciler leaves that
// job unregistered.
type Deps struct {
	Pool       *pgxpool.Pool
	Expirer    Expirer
	Reconciler Reconciler
	Clock      clock.Clock
	Log        *slog.Logger
}

func (d *Deps) defaults() {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
}

// ------------------------------------------------------------------------- maintenance

// Job arguments are stored in River's tables, which the application role can
// read and which carry no row-level security. Arguments must therefore hold
// IDs only: never personal data, message text, tokens or secrets. A job loads
// what it needs inside a tenant transaction when it runs.

// MaintenanceArgs deletes spent rows that keep the identity and webhook
// tables bounded: expired idempotency outcomes, spent refresh tokens, old MFA
// challenges, old inbound webhook dedupe records and old single-use scan
// records.
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
	if err := errors.Join(errs...); err != nil {
		w.deps.Log.ErrorContext(ctx, "maintenance failed", "err", err)
		return err
	}
	w.deps.Log.InfoContext(ctx, "maintenance run", "idempotency_keys", idem, "refresh_tokens", tokens,
		"mfa_challenges", challenges, "webhook_events", events, "scan_token_uses", scans)
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

// PointsReconcileArgs checks every points account against its ledger and
// open lots.
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

// Work runs the check. Mismatches are an ERROR log line: that is the alert.
func (w *PointsReconcileWorker) Work(ctx context.Context, _ *river.Job[PointsReconcileArgs]) error {
	checked, mismatches, err := w.deps.Reconciler.Reconcile(ctx, w.deps.Clock.Now())
	if err != nil {
		w.deps.Log.ErrorContext(ctx, "points reconciliation failed", "err", err)
		return fmt.Errorf("points reconciliation: %w", err)
	}
	if mismatches > 0 {
		w.deps.Log.ErrorContext(ctx, "points reconciliation found mismatches", "accounts_checked", checked, "mismatches", mismatches)
		return nil
	}
	w.deps.Log.InfoContext(ctx, "points reconciliation clean", "accounts_checked", checked)
	return nil
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
	if deps.Reconciler != nil {
		river.AddWorker(w, &PointsReconcileWorker{deps: deps})
	} else {
		deps.Log.Warn("points reconciliation job disabled: no reconciler configured")
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
	if deps.Reconciler != nil {
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

// NewInserter returns an insert-only client for processes that enqueue jobs
// but do not work them (the API, from later phases).
func NewInserter(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("jobs: river inserter: %w", err)
	}
	return client, nil
}
