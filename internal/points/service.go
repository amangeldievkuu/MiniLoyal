// Package points owns the points ledger: earn rules, cached balances, FIFO
// lots and the append-only ledger. Every balance change runs in one tenant
// transaction that first locks the member's account row, so concurrent
// earns, redemptions and expiries serialize per member and the balance can
// never go negative. Corrections are new ledger rows, never updates.
package points

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/events"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Audit actions.
const (
	ActionAdjust      = "points.adjust"
	ActionStaffEarn   = "points.earn"
	ActionStaffRedeem = "points.redeem"
	ActionRules       = "points.rules"
)

// Ledger reasons, which are also the transaction kinds.
const (
	KindEarn   = "earn"
	KindRedeem = "redeem"
	KindAdjust = "adjust"
	KindExpire = "expire"
)

// MaxPointsPerOperation bounds one operation so a lot always fits its column.
const MaxPointsPerOperation = 1_000_000

// MaxEarnPerOperation is the platform ceiling on points one purchase or one
// fixed grant can earn, whatever an app's rule says. Dashboard adjustments
// (audited, owners and admins only) may go up to MaxPointsPerOperation.
const MaxEarnPerOperation = 100_000

// DefaultMaxPointsPerTxn caps an earn for apps that never saved a rule.
const DefaultMaxPointsPerTxn = 10_000

// MaxSpendAmount bounds spend_amount (currency units) in the service, not
// only in the API spec.
const MaxSpendAmount = 100_000_000

var (
	errInsufficient = apierr.New(409, "insufficient_points", "the member does not have enough points")
	errDisabled     = apierr.New(409, "points_disabled", "points are disabled for this app")
	errScanUsed     = apierr.New(409, "scan_used", "this scan already paid for a redemption; scan the member card again")
	errSelfService  = apierr.New(403, "self_service", "staff cannot give or spend points on their own member card")
	errNotFound     = access.ErrMemberNotFound
)

// Deps are the service's collaborators.
type Deps struct {
	Pool    *pgxpool.Pool
	Tenants *tenant.Service
	// Keyring encrypts stored idempotent outcomes. Nil stores plain JSON
	// (tests).
	Keyring crypto.Keyring
	// Card verifies the scan tokens that staff actions carry.
	Card  *card.Codec
	Clock clock.Clock
	Log   *slog.Logger
	// StaffWritesPerMinute limits points writes per staff member. Zero
	// means the default (30); a negative value turns the limit off (tests).
	// Ignored when StaffLimiter is set.
	StaffWritesPerMinute int
	// StaffLimiter is the per-staff write limiter shared with coupons and
	// stamps, so the limit counts every staff write together. Nil builds
	// one from StaffWritesPerMinute.
	StaffLimiter *auth.Limiter
}

// Service is the points domain.
type Service struct {
	pool    *pgxpool.Pool
	tenants *tenant.Service
	writer  *access.Writer
	card    *card.Codec
	clock   clock.Clock
	log     *slog.Logger
	// staffLimit is nil when the per-staff limit is off.
	staffLimit *auth.Limiter
}

// New returns a Service.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	staffLimit := d.StaffLimiter
	if staffLimit == nil {
		staffLimit = access.NewStaffLimiter(d.Clock, d.StaffWritesPerMinute)
	}
	return &Service{
		pool:       d.Pool,
		tenants:    d.Tenants,
		writer:     access.NewWriter(d.Pool, d.Keyring, d.Clock),
		card:       d.Card,
		clock:      d.Clock,
		log:        d.Log,
		staffLimit: staffLimit,
	}
}

// Idem is the client's idempotency key and the request fingerprint.
type Idem = access.Idem

// Txn is one points operation as clients see it.
type Txn struct {
	ID           uuid.UUID  `json:"id"`
	MemberID     uuid.UUID  `json:"member_id"`
	Kind         string     `json:"kind"`
	Points       int64      `json:"points"`
	BalanceAfter int64      `json:"balance_after"`
	SpendAmount  *int       `json:"spend_amount,omitempty"`
	RefType      *string    `json:"ref_type,omitempty"`
	RefID        *string    `json:"ref_id,omitempty"`
	Note         *string    `json:"note,omitempty"`
	StoreID      *uuid.UUID `json:"store_id,omitempty"`
	ActorType    string     `json:"actor_type"`
	CreatedAt    time.Time  `json:"created_at"`
}

// operation is one change to a member's balance.
type operation struct {
	kind        string
	delta       int64 // signed
	expires     bool  // a positive delta creates a lot that follows the rule's expiry
	spend       *int
	refType     *string
	refID       *string
	note        *string
	storeID     uuid.UUID
	actorType   string
	actorID     uuid.UUID
	earnedDelta int64
}

// apply changes the member's balance inside tx, which must be scoped to the
// member's tenant. It locks the account row first, so concurrent operations
// on one member serialize.
func (s *Service) apply(ctx context.Context, q *sqlc.Queries, m sqlc.Member, rules Rules, op operation) (Txn, error) {
	if op.delta == 0 {
		return Txn{}, apierr.Validation(map[string]string{"points": "must not be zero"})
	}
	if op.delta > MaxPointsPerOperation || op.delta < -MaxPointsPerOperation {
		return Txn{}, apierr.Validation(map[string]string{"points": fmt.Sprintf("must be at most %d per operation", MaxPointsPerOperation)})
	}
	if op.kind == KindEarn && op.delta > MaxEarnPerOperation {
		return Txn{}, apierr.Validation(map[string]string{"points": fmt.Sprintf("one purchase earns at most %d points", MaxEarnPerOperation)})
	}
	if err := q.EnsurePointsAccount(ctx, sqlc.EnsurePointsAccountParams{MemberID: m.ID, TenantID: m.TenantID, AppID: m.AppID}); err != nil {
		return Txn{}, fmt.Errorf("points: ensure account: %w", err)
	}
	acct, err := q.LockPointsAccount(ctx, m.ID)
	if err != nil {
		return Txn{}, fmt.Errorf("points: lock account: %w", err)
	}
	now := s.clock.Now()
	// Expired lots are expired before anything else touches the balance, so
	// points past their expiry are never spent even before the job runs.
	current, err := s.expireMemberDue(ctx, q, m, acct.Balance, now)
	if err != nil {
		return Txn{}, err
	}
	txnID := uuid.Must(uuid.NewV7())
	balance := current + op.delta
	if balance < 0 {
		return Txn{}, errInsufficient
	}
	entry := func(lotID *int64, delta int64) error {
		_, err := q.InsertLedgerEntry(ctx, sqlc.InsertLedgerEntryParams{
			TenantID:     m.TenantID,
			AppID:        m.AppID,
			MemberID:     m.ID,
			TxnID:        txnID,
			LotID:        lotID,
			Delta:        int32(delta),
			BalanceAfter: balance,
			Reason:       op.kind,
			SpendAmount:  int32Ptr(op.spend),
			RefType:      op.refType,
			RefID:        op.refID,
			Note:         op.note,
			StoreID:      nullUUID(op.storeID),
			ActorType:    op.actorType,
			ActorID:      nullUUID(op.actorID),
		})
		if err != nil {
			return fmt.Errorf("points: ledger: %w", err)
		}
		return nil
	}

	if op.delta > 0 {
		var expiresAt pgtype.Timestamptz
		if op.expires && rules.ExpiryDays != nil {
			expiresAt = pgtype.Timestamptz{Time: now.Add(time.Duration(*rules.ExpiryDays) * 24 * time.Hour), Valid: true}
		}
		lot, err := q.InsertPointsLot(ctx, sqlc.InsertPointsLotParams{
			TenantID: m.TenantID, AppID: m.AppID, MemberID: m.ID, Earned: int32(op.delta), ExpiresAt: expiresAt,
		})
		if err != nil {
			return Txn{}, fmt.Errorf("points: create lot: %w", err)
		}
		if err := entry(&lot.ID, op.delta); err != nil {
			return Txn{}, err
		}
	} else {
		need := -op.delta
		lots, err := q.ListOpenLotsForUpdate(ctx, m.ID)
		if err != nil {
			return Txn{}, fmt.Errorf("points: open lots: %w", err)
		}
		for _, lot := range lots {
			if need == 0 {
				break
			}
			take := min(int64(lot.Remaining), need)
			if err := q.SetLotRemaining(ctx, sqlc.SetLotRemainingParams{ID: lot.ID, Remaining: lot.Remaining - int32(take)}); err != nil {
				return Txn{}, fmt.Errorf("points: consume lot: %w", err)
			}
			id := lot.ID
			if err := entry(&id, -take); err != nil {
				return Txn{}, err
			}
			need -= take
		}
		if need != 0 {
			// The cached balance promised points the lots do not hold: a
			// reconciliation problem, never something to paper over.
			return Txn{}, fmt.Errorf("points: member %s lots hold %d fewer points than the balance", m.ID, need)
		}
	}
	if err := q.SetPointsBalance(ctx, sqlc.SetPointsBalanceParams{MemberID: m.ID, Balance: balance, EarnedDelta: op.earnedDelta}); err != nil {
		return Txn{}, fmt.Errorf("points: set balance: %w", err)
	}
	if err := emitChanged(ctx, q, m.TenantID, m.AppID, m.ID, txnID, op.kind, op.delta, balance, now); err != nil {
		return Txn{}, err
	}
	t := Txn{
		ID: txnID, MemberID: m.ID, Kind: op.kind, Points: op.delta, BalanceAfter: balance,
		SpendAmount: op.spend, RefType: op.refType, RefID: op.refID, Note: op.note,
		ActorType: op.actorType, CreatedAt: now,
	}
	if op.storeID != uuid.Nil {
		id := op.storeID
		t.StoreID = &id
	}
	return t, nil
}

// write runs fn once per idempotency key and replays the stored outcome
// for retries; authorize runs before claim and replay (access.Writer).
func (s *Service) write(ctx context.Context, tenantID uuid.UUID, scope string, idem Idem, authorize func(ctx context.Context, tx pgx.Tx) error, fn func(ctx context.Context, tx pgx.Tx) (any, error), out any) error {
	return s.writer.Write(ctx, tenantID, scope, idem, authorize, fn, out)
}

// reach builds the access for p on memberID. Dashboard users need at least
// role min; API keys and members stay inside their own app.
func (s *Service) reach(ctx context.Context, p principal.Principal, memberID uuid.UUID, min principal.Role) (access.Access, error) {
	return access.Reach(ctx, s.pool, s.tenants, p, memberID, min)
}

func scopeOf(p principal.Principal) string { return access.ScopeOf(p) }

func actorOf(p principal.Principal) (string, uuid.UUID) { return access.ActorOf(p) }

// emitChanged records points.changed for one transaction of a member, in
// the transaction that made it.
func emitChanged(ctx context.Context, q *sqlc.Queries, tenantID, appID, memberID, txnID uuid.UUID, kind string, delta, balance int64, at time.Time) error {
	return events.Emit(ctx, q, events.Event{
		TenantID: tenantID, AppID: appID, Type: events.PointsChanged, MemberID: memberID, OccurredAt: at,
		Payload: map[string]any{
			"member_id": memberID, "txn_id": txnID, "kind": kind, "points": delta, "balance_after": balance,
		},
	})
}

// Adjust corrects a member's points on a dashboard user's behalf. Owners and
// admins only; audited.
func (s *Service) Adjust(ctx context.Context, p principal.Principal, memberID uuid.UUID, idem Idem, delta int, note string) (Txn, error) {
	if !p.IsUser() {
		return Txn{}, errNotFound
	}
	if fields := validateNote(&note, true); len(fields) > 0 {
		return Txn{}, apierr.Validation(fields)
	}
	if delta == 0 {
		return Txn{}, apierr.Validation(map[string]string{"delta": "must not be zero"})
	}
	acc, err := s.reach(ctx, p, memberID, principal.RoleAdmin)
	if err != nil {
		return Txn{}, err
	}
	var m sqlc.Member
	var out Txn
	err = s.write(ctx, acc.TenantID, scopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) { m, err = acc.Check(ctx, tx); return err },
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
			rules, err := rulesFor(ctx, q, m.AppID)
			if err != nil {
				return nil, err
			}
			actorType, actorID := actorOf(p)
			t, err := s.apply(ctx, q, m, rules, operation{kind: KindAdjust, delta: int64(delta), note: &note, actorType: actorType, actorID: actorID})
			if err != nil {
				return nil, err
			}
			appID := m.AppID
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: m.TenantID, AppID: &appID, Actor: p, Action: ActionAdjust,
				TargetType: "member", TargetID: m.ID.String(),
				// No note: audit rows outlive a PDPA erasure.
				Metadata: map[string]any{"delta": delta, "txn_id": t.ID.String()},
			}); err != nil {
				return nil, err
			}
			return t, nil
		}, &out)
	return out, err
}

// expireMemberDue expires the member's lots whose expiry passed, inside the
// caller's transaction, which already holds the member's account lock. It
// writes one expire row per lot (the unique index keeps that to one per
// lot) and returns the balance afterwards.
func (s *Service) expireMemberDue(ctx context.Context, q *sqlc.Queries, m sqlc.Member, balance int64, now time.Time) (int64, error) {
	lots, err := q.ListMemberDueLotsForUpdate(ctx, sqlc.ListMemberDueLotsForUpdateParams{MemberID: m.ID, Now: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return 0, fmt.Errorf("points: due lots: %w", err)
	}
	if len(lots) == 0 {
		return balance, nil
	}
	for _, lot := range lots {
		balance -= int64(lot.Remaining)
		if balance < 0 {
			return 0, fmt.Errorf("points: member %s balance is below its expiring lot %d", m.ID, lot.ID)
		}
		lotID := lot.ID
		txnID := uuid.Must(uuid.NewV7())
		if _, err := q.InsertLedgerEntry(ctx, sqlc.InsertLedgerEntryParams{
			TenantID: lot.TenantID, AppID: lot.AppID, MemberID: lot.MemberID,
			TxnID: txnID, LotID: &lotID, Delta: -lot.Remaining, BalanceAfter: balance,
			Reason: KindExpire, ActorType: "system",
		}); err != nil {
			return 0, fmt.Errorf("points: expire lot %d: %w", lot.ID, err)
		}
		if err := emitChanged(ctx, q, lot.TenantID, lot.AppID, lot.MemberID, txnID, KindExpire, -int64(lot.Remaining), balance, now); err != nil {
			return 0, err
		}
		if err := q.SetLotRemaining(ctx, sqlc.SetLotRemainingParams{ID: lot.ID, Remaining: 0}); err != nil {
			return 0, fmt.Errorf("points: empty lot %d: %w", lot.ID, err)
		}
	}
	if err := q.SetPointsBalance(ctx, sqlc.SetPointsBalanceParams{MemberID: m.ID, Balance: balance}); err != nil {
		return 0, fmt.Errorf("points: set balance after expiry: %w", err)
	}
	return balance, nil
}
