package points

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// History paging limits.
const (
	DefaultLimit = 50
	MaxLimit     = 100
)

// Expiring is a batch of points that expire at the same instant.
type Expiring struct {
	Points    int64
	ExpiresAt time.Time
}

// Summary is a member's balance, what expires next and the earn rule.
type Summary struct {
	MemberID       uuid.UUID
	Balance        int64
	LifetimeEarned int64
	Expiring       []Expiring
	Rules          Rules
}

// Page is one page of transactions, newest first.
type Page struct {
	Items      []Txn
	NextCursor string
}

// read runs fn for a member p may read: a member reads only themselves, an
// API key only members of its app, a dashboard user any member of a tenant
// they belong to.
func (s *Service) read(ctx context.Context, p principal.Principal, memberID uuid.UUID, fn func(ctx context.Context, q *sqlc.Queries, m sqlc.Member) error) error {
	acc, err := s.reach(ctx, p, memberID, principal.RoleStaff)
	if err != nil {
		return err
	}
	return db.WithTenant(ctx, s.pool, acc.tenantID, func(ctx context.Context, tx pgx.Tx) error {
		m, err := acc.check(ctx, tx)
		if err != nil {
			return err
		}
		return fn(ctx, sqlc.New(tx), m)
	})
}

// Summary returns the member's balance, the next expiries and the rule.
func (s *Service) Summary(ctx context.Context, p principal.Principal, memberID uuid.UUID) (Summary, error) {
	var out Summary
	err := s.read(ctx, p, memberID, func(ctx context.Context, q *sqlc.Queries, m sqlc.Member) error {
		out.MemberID = m.ID
		acct, err := q.GetPointsAccount(ctx, m.ID)
		switch {
		case db.IsNotFound(err):
		case err != nil:
			return fmt.Errorf("points: account: %w", err)
		default:
			out.Balance, out.LifetimeEarned = acct.Balance, acct.LifetimeEarned
		}
		rows, err := q.ListExpiringSoon(ctx, sqlc.ListExpiringSoonParams{MemberID: m.ID, Now: pgtype.Timestamptz{Time: s.clock.Now(), Valid: true}})
		if err != nil {
			return fmt.Errorf("points: expiring: %w", err)
		}
		out.Expiring = make([]Expiring, 0, len(rows))
		for _, r := range rows {
			out.Expiring = append(out.Expiring, Expiring{Points: r.Points, ExpiresAt: r.ExpiresAt.Time})
		}
		out.Rules, err = rulesFor(ctx, q, m.AppID)
		return err
	})
	return out, err
}

// History returns the member's transactions, newest first.
func (s *Service) History(ctx context.Context, p principal.Principal, memberID uuid.UUID, limit int, cursor string) (Page, error) {
	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 1 || limit > MaxLimit:
		return Page{}, apierr.BadRequest("limit must be between 1 and 100")
	}
	var before *int64
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return Page{}, apierr.BadRequest("invalid cursor")
		}
		id, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || id < 1 {
			return Page{}, apierr.BadRequest("invalid cursor")
		}
		before = &id
	}
	var out Page
	err := s.read(ctx, p, memberID, func(ctx context.Context, q *sqlc.Queries, m sqlc.Member) error {
		heads, err := q.ListPointsTxnPage(ctx, sqlc.ListPointsTxnPageParams{MemberID: m.ID, BeforeID: before, PageLimit: int32(limit + 1)})
		if err != nil {
			return fmt.Errorf("points: history: %w", err)
		}
		if len(heads) > limit {
			heads = heads[:limit]
			out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(heads[len(heads)-1].LastID, 10)))
		}
		ids := make([]uuid.UUID, len(heads))
		for i, h := range heads {
			ids[i] = h.TxnID
		}
		rows, err := q.ListLedgerByTxns(ctx, sqlc.ListLedgerByTxnsParams{MemberID: m.ID, TxnIds: ids})
		if err != nil {
			return fmt.Errorf("points: history rows: %w", err)
		}
		byTxn := map[uuid.UUID]*Txn{}
		for _, r := range rows {
			t, ok := byTxn[r.TxnID]
			if !ok {
				t = &Txn{ID: r.TxnID, MemberID: r.MemberID, Kind: r.Reason, ActorType: r.ActorType, CreatedAt: r.CreatedAt.Time,
					RefType: r.RefType, RefID: r.RefID, Note: r.Note}
				if r.SpendAmount != nil {
					v := int(*r.SpendAmount)
					t.SpendAmount = &v
				}
				if r.StoreID.Valid {
					id := r.StoreID.UUID
					t.StoreID = &id
				}
				byTxn[r.TxnID] = t
			}
			t.Points += int64(r.Delta)
			t.BalanceAfter = r.BalanceAfter
		}
		out.Items = make([]Txn, 0, len(heads))
		for _, h := range heads {
			if t, ok := byTxn[h.TxnID]; ok {
				// Notes and references are for the brand (adjustment reasons,
				// receipt numbers); members see what happened, not why.
				if p.IsMember() {
					t.Note, t.RefID = nil, nil
				}
				out.Items = append(out.Items, *t)
			}
		}
		return nil
	})
	return out, err
}
