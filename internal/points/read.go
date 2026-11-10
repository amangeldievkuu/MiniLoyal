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
		var err error
		out, err = summaryOf(ctx, q, m, s.clock.Now())
		return err
	})
	return out, err
}

func summaryOf(ctx context.Context, q *sqlc.Queries, m sqlc.Member, now time.Time) (Summary, error) {
	out := Summary{MemberID: m.ID}
	at := pgtype.Timestamptz{Time: now, Valid: true}
	acct, err := q.GetPointsAccount(ctx, m.ID)
	switch {
	case db.IsNotFound(err):
	case err != nil:
		return Summary{}, fmt.Errorf("points: account: %w", err)
	default:
		out.Balance, out.LifetimeEarned = acct.Balance, acct.LifetimeEarned
		due, err := q.SumMemberDuePoints(ctx, sqlc.SumMemberDuePointsParams{MemberID: m.ID, Now: at})
		if err != nil {
			return Summary{}, fmt.Errorf("points: due points: %w", err)
		}
		out.Balance -= due
	}
	rows, err := q.ListExpiringSoon(ctx, sqlc.ListExpiringSoonParams{MemberID: m.ID, Now: at})
	if err != nil {
		return Summary{}, fmt.Errorf("points: expiring: %w", err)
	}
	out.Expiring = make([]Expiring, 0, len(rows))
	for _, r := range rows {
		out.Expiring = append(out.Expiring, Expiring{Points: r.Points, ExpiresAt: r.ExpiresAt.Time})
	}
	out.Rules, err = rulesFor(ctx, q, m.AppID)
	if err != nil {
		return Summary{}, err
	}
	return out, nil
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
		items, last, more, err := txnPage(ctx, q, m.ID, before, limit, p.IsMember())
		if err != nil {
			return err
		}
		out.Items = items
		if more {
			out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last, 10)))
		}
		return nil
	})
	return out, err
}

// exportPage is the page size used to walk the whole ledger for an export.
const exportPage = 500

// MemberData is the points part of a member's PDPA export.
type MemberData struct {
	Summary Summary
	History []Txn
}

// ExportMember reads the member's summary and every transaction, newest
// first, inside the caller's tenant transaction, so the export is one
// consistent snapshot. The caller has already authorized access to m.
// forMember drops notes and references as the member's own history does.
func ExportMember(ctx context.Context, q *sqlc.Queries, m sqlc.Member, now time.Time, forMember bool) (MemberData, error) {
	sum, err := summaryOf(ctx, q, m, now)
	if err != nil {
		return MemberData{}, err
	}
	out := MemberData{Summary: sum, History: []Txn{}}
	var before *int64
	for {
		items, last, more, err := txnPage(ctx, q, m.ID, before, exportPage, forMember)
		if err != nil {
			return MemberData{}, err
		}
		out.History = append(out.History, items...)
		if !more {
			return out, nil
		}
		before = &last
	}
}

// txnPage reads up to limit transactions older than before, newest first.
// last is the keyset of the last transaction returned and more reports
// whether older ones exist.
func txnPage(ctx context.Context, q *sqlc.Queries, memberID uuid.UUID, before *int64, limit int, forMember bool) (items []Txn, last int64, more bool, err error) {
	heads, err := q.ListPointsTxnPage(ctx, sqlc.ListPointsTxnPageParams{MemberID: memberID, BeforeID: before, PageLimit: int32(limit + 1)})
	if err != nil {
		return nil, 0, false, fmt.Errorf("points: history: %w", err)
	}
	if len(heads) > limit {
		heads = heads[:limit]
		more = true
	}
	if len(heads) > 0 {
		last = heads[len(heads)-1].LastID
	}
	ids := make([]uuid.UUID, len(heads))
	for i, h := range heads {
		ids[i] = h.TxnID
	}
	rows, err := q.ListLedgerByTxns(ctx, sqlc.ListLedgerByTxnsParams{MemberID: memberID, TxnIds: ids})
	if err != nil {
		return nil, 0, false, fmt.Errorf("points: history rows: %w", err)
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
	items = make([]Txn, 0, len(heads))
	for _, h := range heads {
		if t, ok := byTxn[h.TxnID]; ok {
			// Notes and references are for the brand (adjustment reasons,
			// receipt numbers); members see what happened, not why.
			if forMember {
				t.Note, t.RefID = nil, nil
			}
			items = append(items, *t)
		}
	}
	return items, last, more, nil
}
