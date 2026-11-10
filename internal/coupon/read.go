package coupon

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Offers lists the templates the signed-in member can claim now.
func (s *Service) Offers(ctx context.Context, p principal.Principal) ([]Offer, error) {
	if !p.IsMember() {
		return nil, access.ErrMemberNotFound
	}
	out := []Offer{}
	err := access.Read(ctx, s.pool, s.tenants, p, p.MemberID, principal.RoleStaff, func(ctx context.Context, tx pgx.Tx, m sqlc.Member) error {
		rows, err := sqlc.New(tx).ListCouponOffers(ctx, sqlc.ListCouponOffersParams{MemberID: m.ID, AppID: m.AppID, Now: ts(s.clock.Now())})
		if err != nil {
			return fmt.Errorf("coupon: offers: %w", err)
		}
		for _, r := range rows {
			out = append(out, Offer{
				Template:       Summary{ID: r.ID, Name: r.Name, Description: r.Description, Type: r.Type, Value: intOf(r.Value)},
				ValidTo:        timeOf(r.ValidTo),
				ValidityDays:   intOf(r.ValidityDays),
				PerMemberLimit: int(r.PerMemberLimit),
				Held:           int(r.Held),
				// per_member_limit counts the member's own claims only.
				CanClaim: r.HeldFromClaims < r.PerMemberLimit,
			})
		}
		return nil
	})
	return out, err
}

// memberStates maps the member list's filter to effective states.
var memberStates = map[string]string{StateAvailable: StateAvailable, "used": StatusRedeemed, StateExpired: StateExpired}

// ListMine pages through the signed-in member's coupons, newest first.
// Voided coupons are never shown to the member; state is available, used
// or expired.
func (s *Service) ListMine(ctx context.Context, p principal.Principal, state *string, limit int, cursor string) (Page, error) {
	if !p.IsMember() {
		return Page{}, access.ErrMemberNotFound
	}
	var filter *string
	if state != nil {
		st, ok := memberStates[*state]
		if !ok {
			return Page{}, apierr.BadRequest("state must be available, used or expired")
		}
		filter = &st
	}
	return s.list(ctx, p, p.MemberID, filter, true, limit, cursor)
}

// ListMember pages through a member's coupons, voided ones included, for a
// dashboard user (any role) or an API key of the member's app.
func (s *Service) ListMember(ctx context.Context, p principal.Principal, memberID uuid.UUID, state *string, limit int, cursor string) (Page, error) {
	if !p.IsUser() && !p.IsAPIKey() {
		return Page{}, access.ErrMemberNotFound
	}
	if state != nil {
		switch *state {
		case StateAvailable, StatusRedeemed, StateExpired, StatusVoided:
		default:
			return Page{}, apierr.BadRequest("state must be available, redeemed, expired or voided")
		}
	}
	return s.list(ctx, p, memberID, state, false, limit, cursor)
}

func (s *Service) list(ctx context.Context, p principal.Principal, memberID uuid.UUID, state *string, excludeVoided bool, limit int, cursor string) (Page, error) {
	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 1 || limit > MaxLimit:
		return Page{}, apierr.BadRequest("limit must be between 1 and 100")
	}
	beforeAt, beforeID, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	var out Page
	err = access.Read(ctx, s.pool, s.tenants, p, memberID, principal.RoleStaff, func(ctx context.Context, tx pgx.Tx, m sqlc.Member) error {
		items, more, err := page(ctx, sqlc.New(tx), m, state, excludeVoided, beforeAt, beforeID, limit, s.clock.Now())
		if err != nil {
			return err
		}
		out.Items = items
		if more {
			last := items[len(items)-1]
			out.NextCursor = encodeCursor(last.IssuedAt, last.ID)
		}
		return nil
	})
	return out, err
}

// page reads up to limit coupons issued before the keyset, newest first.
func page(ctx context.Context, q *sqlc.Queries, m sqlc.Member, state *string, excludeVoided bool, beforeAt pgtype.Timestamptz, beforeID uuid.NullUUID, limit int, now time.Time) ([]Instance, bool, error) {
	rows, err := q.ListMemberCoupons(ctx, sqlc.ListMemberCouponsParams{
		Now: ts(now), AppID: m.AppID, MemberID: m.ID, ExcludeVoided: excludeVoided, State: state,
		BeforeIssuedAt: beforeAt, BeforeID: beforeID, PageLimit: int32(limit + 1),
	})
	if err != nil {
		return nil, false, fmt.Errorf("coupon: list: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := make([]Instance, 0, len(rows))
	for _, r := range rows {
		inst := Instance{
			ID: r.ID, Code: r.Code, MemberID: r.MemberID, Status: r.Status, State: r.State, Source: r.Source,
			IssuedAt: r.IssuedAt.Time, ExpiresAt: timeOf(r.ExpiresAt), RedeemedAt: timeOf(r.RedeemedAt),
			Template: Summary{ID: r.TemplateID, Name: r.TemplateName, Description: r.TemplateDescription, Type: r.TemplateType, Value: intOf(r.TemplateValue)},
		}
		if r.RedeemedAtStoreID.Valid && r.StoreName != nil {
			inst.Store = &StoreRef{ID: r.RedeemedAtStoreID.UUID, Name: *r.StoreName}
		}
		items = append(items, inst)
	}
	return items, more, nil
}

// Redeemable lists what staff can redeem for the member now, soonest
// expiry first, inside the caller's tenant transaction (the scan).
func Redeemable(ctx context.Context, q *sqlc.Queries, m sqlc.Member, now time.Time) ([]Instance, error) {
	rows, err := q.ListRedeemableMemberCoupons(ctx, sqlc.ListRedeemableMemberCouponsParams{
		AppID: m.AppID, MemberID: m.ID, Now: ts(now), PageLimit: MaxRedeemable,
	})
	if err != nil {
		return nil, fmt.Errorf("coupon: redeemable: %w", err)
	}
	out := make([]Instance, 0, len(rows))
	for _, r := range rows {
		out = append(out, Instance{
			ID: r.ID, Code: r.Code, MemberID: r.MemberID, Status: StatusIssued, State: StateAvailable, Source: r.Source,
			IssuedAt: r.IssuedAt.Time, ExpiresAt: timeOf(r.ExpiresAt),
			Template: Summary{ID: r.TemplateID, Name: r.TemplateName, Description: r.TemplateDescription, Type: r.TemplateType, Value: intOf(r.TemplateValue)},
		})
	}
	return out, nil
}

// ExportMember reads every coupon the member holds or held, voided ones
// included, newest first, inside the caller's tenant transaction. The
// caller has already authorized access to m.
func ExportMember(ctx context.Context, q *sqlc.Queries, m sqlc.Member, now time.Time) ([]Instance, error) {
	out := []Instance{}
	var beforeAt pgtype.Timestamptz
	var beforeID uuid.NullUUID
	for {
		items, more, err := page(ctx, q, m, nil, false, beforeAt, beforeID, exportPage, now)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if !more {
			return out, nil
		}
		last := items[len(items)-1]
		beforeAt, beforeID = ts(last.IssuedAt), uuid.NullUUID{UUID: last.ID, Valid: true}
	}
}

// The cursor is the keyset (issued_at, id) of the last coupon returned.
func encodeCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (pgtype.Timestamptz, uuid.NullUUID, error) {
	if c == "" {
		return pgtype.Timestamptz{}, uuid.NullUUID{}, nil
	}
	bad := apierr.BadRequest("invalid cursor")
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return pgtype.Timestamptz{}, uuid.NullUUID{}, bad
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return pgtype.Timestamptz{}, uuid.NullUUID{}, bad
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return pgtype.Timestamptz{}, uuid.NullUUID{}, bad
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return pgtype.Timestamptz{}, uuid.NullUUID{}, bad
	}
	return ts(t), uuid.NullUUID{UUID: u, Valid: true}, nil
}

// DeleteOldScanRedeems removes coupon redeem counts of scans older than a
// day; scan tokens live five minutes. The maintenance job runs it.
func DeleteOldScanRedeems(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int64, error) {
	var n int64
	err := db.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		n, err = sqlc.New(tx).DeleteOldScanCouponRedeems(ctx, ts(now.Add(-24*time.Hour)))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("coupon: delete old scan redeems: %w", err)
	}
	return n, nil
}
