// Package points owns the points ledger: earn rules, cached balances, FIFO
// lots and the append-only ledger. Every balance change runs in one tenant
// transaction that first locks the member's account row, so concurrent
// earns, redemptions and expiries serialize per member and the balance can
// never go negative. Corrections are new ledger rows, never updates.
package points

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Audit actions.
const (
	ActionAdjust = "points.adjust"
	ActionRules  = "points.rules"
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

var (
	errInsufficient = apierr.New(409, "insufficient_points", "the member does not have enough points")
	errDisabled     = apierr.New(409, "points_disabled", "points are disabled for this app")
	errScanInvalid  = apierr.New(403, "scan_invalid", "scan the member card again")
	errScanMismatch = apierr.New(403, "scan_mismatch", "this scan belongs to another staff member or app")
	errNotFound     = apierr.NotFound("member")
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
}

// Service is the points domain.
type Service struct {
	pool    *pgxpool.Pool
	tenants *tenant.Service
	idem    *idempotency.Store
	card    *card.Codec
	clock   clock.Clock
	log     *slog.Logger
}

// New returns a Service.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	var codec idempotency.Codec
	if d.Keyring != nil {
		codec = idempotency.NewEncryptedCodec(d.Keyring)
	}
	return &Service{
		pool:    d.Pool,
		tenants: d.Tenants,
		idem:    idempotency.New(d.Pool, codec, d.Clock),
		card:    d.Card,
		clock:   d.Clock,
		log:     d.Log,
	}
}

// Idem is the client's idempotency key and the request fingerprint.
type Idem struct {
	Key  string
	Hash []byte
}

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
	if err := q.EnsurePointsAccount(ctx, sqlc.EnsurePointsAccountParams{MemberID: m.ID, TenantID: m.TenantID, AppID: m.AppID}); err != nil {
		return Txn{}, fmt.Errorf("points: ensure account: %w", err)
	}
	acct, err := q.LockPointsAccount(ctx, m.ID)
	if err != nil {
		return Txn{}, fmt.Errorf("points: lock account: %w", err)
	}
	now := s.clock.Now()
	txnID := uuid.Must(uuid.NewV7())
	balance := acct.Balance + op.delta
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

// write resolves nothing itself: tenantID and the authorization hook come
// from the caller. It runs fn once per idempotency key and replays the
// stored outcome for retries; authorize runs before claim and replay.
func (s *Service) write(ctx context.Context, tenantID uuid.UUID, scope string, idem Idem, authorize func(ctx context.Context, tx pgx.Tx) error, fn func(ctx context.Context, tx pgx.Tx) (any, error), out any) error {
	res, err := s.idem.Run(ctx, idempotency.Request{
		TenantID: tenantID,
		Scope:    scope,
		Key:      idem.Key,
		Hash:     idem.Hash,
		Before:   authorize,
	}, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		v, err := fn(ctx, tx)
		if err != nil {
			return 0, nil, err
		}
		return 200, v, nil
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return fmt.Errorf("points: decode outcome: %w", err)
	}
	return nil
}

// loadMember reads the member inside a tenant transaction and checks it
// belongs to appID, is not erased, and that the app is active.
func loadMember(ctx context.Context, q *sqlc.Queries, memberID, appID uuid.UUID) (sqlc.Member, error) {
	m, err := q.GetMember(ctx, memberID)
	if err != nil {
		if db.IsNotFound(err) {
			return sqlc.Member{}, errNotFound
		}
		return sqlc.Member{}, fmt.Errorf("points: load member: %w", err)
	}
	if (appID != uuid.Nil && m.AppID != appID) || m.AnonymizedAt.Valid {
		return sqlc.Member{}, errNotFound
	}
	app, err := q.GetApp(ctx, m.AppID)
	if err != nil {
		return sqlc.Member{}, fmt.Errorf("points: load app: %w", err)
	}
	if app.Status != "active" {
		return sqlc.Member{}, errNotFound
	}
	return m, nil
}

// memberTenant resolves a member to its tenant with a system lookup.
func (s *Service) memberTenant(ctx context.Context, memberID uuid.UUID) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		id, err := sqlc.New(tx).GetMemberTenant(ctx, memberID)
		if err != nil {
			if db.IsNotFound(err) {
				return errNotFound
			}
			return fmt.Errorf("points: member tenant: %w", err)
		}
		tenantID = id
		return nil
	})
	return tenantID, err
}

// access describes how a caller reaches a member: the tenant to scope the
// transaction to, and a check run inside it that loads the member.
type access struct {
	tenantID uuid.UUID
	check    func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error)
}

// reach builds the access for p on memberID. Dashboard users need at least
// role min; API keys and members stay inside their own app.
func (s *Service) reach(ctx context.Context, p principal.Principal, memberID uuid.UUID, min principal.Role) (access, error) {
	switch {
	case p.IsMember():
		if memberID != p.MemberID {
			return access{}, errNotFound
		}
		return access{tenantID: p.TenantID, check: func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error) {
			return loadMember(ctx, sqlc.New(tx), memberID, p.AppID)
		}}, nil
	case p.IsAPIKey():
		tenantID, err := s.memberTenant(ctx, memberID)
		if err != nil {
			return access{}, err
		}
		if tenantID != p.TenantID {
			return access{}, errNotFound
		}
		return access{tenantID: tenantID, check: func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error) {
			return loadMember(ctx, sqlc.New(tx), memberID, p.AppID)
		}}, nil
	case p.IsUser():
		tenantID, err := s.memberTenant(ctx, memberID)
		if err != nil {
			return access{}, err
		}
		return access{tenantID: tenantID, check: func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error) {
			if _, err := s.tenants.Authorize(ctx, tx, p, tenantID, min); err != nil {
				return sqlc.Member{}, err
			}
			return loadMember(ctx, sqlc.New(tx), memberID, uuid.Nil)
		}}, nil
	default:
		return access{}, errNotFound
	}
}

func scopeOf(p principal.Principal) string {
	switch {
	case p.IsStaff():
		return idempotency.Scope("staff", p.StaffID)
	case p.IsAPIKey():
		return idempotency.Scope("apikey", p.APIKeyID)
	case p.IsMember():
		return idempotency.Scope("member", p.MemberID)
	default:
		return idempotency.Scope("user", p.UserID)
	}
}

func actorOf(p principal.Principal) (string, uuid.UUID) {
	switch {
	case p.IsStaff():
		return "staff", p.StaffID
	case p.IsAPIKey():
		return "api_key", p.APIKeyID
	case p.IsMember():
		return "member", p.MemberID
	case p.IsUser():
		return "user", p.UserID
	default:
		return "system", uuid.Nil
	}
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
	err = s.write(ctx, acc.tenantID, scopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) { m, err = acc.check(ctx, tx); return err },
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
				Metadata: map[string]any{"delta": delta, "note": note, "txn_id": t.ID.String()},
			}); err != nil {
				return nil, err
			}
			return t, nil
		}, &out)
	return out, err
}
