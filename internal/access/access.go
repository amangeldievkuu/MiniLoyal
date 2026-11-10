// Package access holds what points, coupons and stamps share about reaching
// a member: idempotent writes, the principal scoping rules, and the staff
// scan checks. It was lifted out of internal/points unchanged, so every
// domain answers the same caller the same way.
package access

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// ErrMemberNotFound is the one answer for a member the caller cannot reach.
var ErrMemberNotFound = apierr.NotFound("member")

// Idem is the client's idempotency key and the request fingerprint.
type Idem struct {
	Key  string
	Hash []byte
}

// Writer runs idempotent writes.
type Writer struct {
	idem *idempotency.Store
}

// NewWriter returns a Writer. Outcomes are stored encrypted when keyring is
// set; nil stores plain JSON (tests).
func NewWriter(pool *pgxpool.Pool, keyring crypto.Keyring, clk clock.Clock) *Writer {
	var codec idempotency.Codec
	if keyring != nil {
		codec = idempotency.NewEncryptedCodec(keyring)
	}
	return &Writer{idem: idempotency.New(pool, codec, clk)}
}

// Write resolves nothing itself: tenantID and the authorization hook come
// from the caller. It runs fn once per idempotency key and replays the
// stored outcome for retries; authorize runs before claim and replay. The
// outcome, fresh or replayed, is decoded into out, so both paths return the
// same document.
func (w *Writer) Write(ctx context.Context, tenantID uuid.UUID, scope string, idem Idem, authorize func(ctx context.Context, tx pgx.Tx) error, fn func(ctx context.Context, tx pgx.Tx) (any, error), out any) error {
	res, err := w.idem.Run(ctx, idempotency.Request{
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
		return fmt.Errorf("access: decode outcome: %w", err)
	}
	return nil
}

// LoadMember reads the member inside a tenant transaction and checks it
// belongs to appID (uuid.Nil skips that check), is not erased, and that the
// app is active.
func LoadMember(ctx context.Context, q *sqlc.Queries, memberID, appID uuid.UUID) (sqlc.Member, error) {
	m, err := q.GetMember(ctx, memberID)
	if err != nil {
		if db.IsNotFound(err) {
			return sqlc.Member{}, ErrMemberNotFound
		}
		return sqlc.Member{}, fmt.Errorf("access: load member: %w", err)
	}
	if (appID != uuid.Nil && m.AppID != appID) || m.AnonymizedAt.Valid {
		return sqlc.Member{}, ErrMemberNotFound
	}
	app, err := q.GetApp(ctx, m.AppID)
	if err != nil {
		return sqlc.Member{}, fmt.Errorf("access: load app: %w", err)
	}
	if app.Status != "active" {
		return sqlc.Member{}, ErrMemberNotFound
	}
	return m, nil
}

// ShareMember takes a FOR SHARE lock on the member row and refuses an erased
// member with ErrMemberNotFound. Writes that store staff free text about the
// member call it before writing, so a PDPA erasure, which updates the row,
// either waits for them and then blanks their text, or runs first and they
// see anonymized_at.
func ShareMember(ctx context.Context, q *sqlc.Queries, appID, memberID uuid.UUID) error {
	anonymizedAt, err := q.ShareLockMember(ctx, sqlc.ShareLockMemberParams{AppID: appID, ID: memberID})
	if err != nil {
		if db.IsNotFound(err) {
			return ErrMemberNotFound
		}
		return fmt.Errorf("access: lock member: %w", err)
	}
	if anonymizedAt.Valid {
		return ErrMemberNotFound
	}
	return nil
}

// MemberTenant resolves a member to its tenant with a system lookup.
func MemberTenant(ctx context.Context, pool *pgxpool.Pool, memberID uuid.UUID) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := db.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		id, err := sqlc.New(tx).GetMemberTenant(ctx, memberID)
		if err != nil {
			if db.IsNotFound(err) {
				return ErrMemberNotFound
			}
			return fmt.Errorf("access: member tenant: %w", err)
		}
		tenantID = id
		return nil
	})
	return tenantID, err
}

// Access describes how a caller reaches a member: the tenant to scope the
// transaction to, and a check run inside it that loads the member.
type Access struct {
	TenantID uuid.UUID
	Check    func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error)
}

// Reach builds the access for p on memberID. Dashboard users need at least
// role min; API keys and members stay inside their own app. Staff reach
// members only through a scan (see StaffMember).
func Reach(ctx context.Context, pool *pgxpool.Pool, tenants *tenant.Service, p principal.Principal, memberID uuid.UUID, min principal.Role) (Access, error) {
	switch {
	case p.IsMember():
		if memberID != p.MemberID {
			return Access{}, ErrMemberNotFound
		}
		return Access{TenantID: p.TenantID, Check: func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error) {
			return LoadMember(ctx, sqlc.New(tx), memberID, p.AppID)
		}}, nil
	case p.IsAPIKey():
		tenantID, err := MemberTenant(ctx, pool, memberID)
		if err != nil {
			return Access{}, err
		}
		if tenantID != p.TenantID {
			return Access{}, ErrMemberNotFound
		}
		return Access{TenantID: tenantID, Check: func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error) {
			return LoadMember(ctx, sqlc.New(tx), memberID, p.AppID)
		}}, nil
	case p.IsUser():
		tenantID, err := MemberTenant(ctx, pool, memberID)
		if err != nil {
			return Access{}, err
		}
		return Access{TenantID: tenantID, Check: func(ctx context.Context, tx pgx.Tx) (sqlc.Member, error) {
			if _, err := tenants.Authorize(ctx, tx, p, tenantID, min); err != nil {
				return sqlc.Member{}, err
			}
			return LoadMember(ctx, sqlc.New(tx), memberID, uuid.Nil)
		}}, nil
	default:
		return Access{}, ErrMemberNotFound
	}
}

// Read runs fn on a member p may reach with at least role min, inside one
// tenant transaction.
func Read(ctx context.Context, pool *pgxpool.Pool, tenants *tenant.Service, p principal.Principal, memberID uuid.UUID, min principal.Role, fn func(ctx context.Context, tx pgx.Tx, m sqlc.Member) error) error {
	acc, err := Reach(ctx, pool, tenants, p, memberID, min)
	if err != nil {
		return err
	}
	return db.WithTenant(ctx, pool, acc.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		m, err := acc.Check(ctx, tx)
		if err != nil {
			return err
		}
		return fn(ctx, tx, m)
	})
}

// ScopeOf names the principal for idempotency keys.
func ScopeOf(p principal.Principal) string {
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

// ActorOf names the principal for ledger and event rows.
func ActorOf(p principal.Principal) (string, uuid.UUID) {
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

// NullUUID is id as a nullable column value; uuid.Nil is NULL.
func NullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}
