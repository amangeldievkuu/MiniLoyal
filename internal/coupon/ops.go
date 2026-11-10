package coupon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/events"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Redeem marks one of the scanned member's coupons used at the counter.
//
// The scan token must be this staff member's, for this app; it names the
// member, and the coupon must be that member's. One conditional UPDATE
// does the redeem, so of any number of concurrent attempts exactly one
// gets the row; every other case (unknown, another member's or app's,
// redeemed, voided, expired) is the same 409 coupon_unavailable. The scan
// is not used up: one scan can redeem several coupons within its lifetime.
// A retry with the same Idempotency-Key replays the stored outcome even
// after the scan expired; authorization still runs first.
func (s *Service) Redeem(ctx context.Context, p principal.Principal, instanceID uuid.UUID, idem Idem, scanToken string) (Instance, error) {
	cl, err := access.VerifyScan(s.card, p, scanToken)
	if err != nil {
		return Instance{}, err
	}
	if err := access.AllowStaff(s.staffLimit, p); err != nil {
		return Instance{}, err
	}
	var m sqlc.Member
	var out Instance
	err = s.writer.Write(ctx, p.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) {
			m, err = access.StaffMember(ctx, sqlc.New(tx), p, cl, errSelfService)
			if errors.Is(err, access.ErrMemberNotFound) {
				// The scanned member was erased (or the app disabled) since
				// the scan: the scan no longer names anyone to redeem for.
				return access.ErrScanInvalid
			}
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			now := s.clock.Now()
			if err := access.ScanFresh(cl, m, now); err != nil {
				return nil, err
			}
			q := sqlc.New(tx)
			row, err := q.RedeemCouponInstance(ctx, sqlc.RedeemCouponInstanceParams{
				Now: ts(now), StaffID: access.NullUUID(p.StaffID), StoreID: access.NullUUID(p.StoreID),
				AppID: p.AppID, ID: instanceID, MemberID: m.ID,
			})
			if err != nil {
				if db.IsNotFound(err) {
					return nil, errUnavailable
				}
				return nil, fmt.Errorf("coupon: redeem: %w", err)
			}
			inst, err := loadInstance(ctx, q, row, now)
			if err != nil {
				return nil, err
			}
			payload := map[string]any{"coupon_id": row.ID, "template_id": row.TemplateID, "member_id": m.ID, "staff_id": p.StaffID}
			if p.StoreID != uuid.Nil {
				payload["store_id"] = p.StoreID
			}
			if err := events.Emit(ctx, q, events.Event{
				TenantID: m.TenantID, AppID: m.AppID, Type: events.CouponRedeemed, MemberID: m.ID, OccurredAt: now, Payload: payload,
			}); err != nil {
				return nil, err
			}
			appID := m.AppID
			meta := map[string]any{"coupon_id": row.ID.String(), "template_id": row.TemplateID.String(), "staff_id": p.StaffID.String()}
			if p.StoreID != uuid.Nil {
				meta["store_id"] = p.StoreID.String()
			}
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: m.TenantID, AppID: &appID, Actor: p, Action: ActionRedeem,
				TargetType: "member", TargetID: m.ID.String(), Metadata: meta,
			}); err != nil {
				return nil, err
			}
			return inst, nil
		}, &out)
	return out, err
}

// Void cancels an issued coupon (expired or not) for a dashboard owner or
// admin; audited with the reason. A void does not give total_limit back.
func (s *Service) Void(ctx context.Context, p principal.Principal, instanceID uuid.UUID, idem Idem, reason string) (Instance, error) {
	if !p.IsUser() {
		return Instance{}, errCouponNotFound
	}
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > 200 || !validText(reason) {
		return Instance{}, apierr.Validation(map[string]string{"reason": "must be 1 to 200 characters of text"})
	}
	var sc sqlc.GetCouponInstanceScopeRow
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		sc, err = sqlc.New(tx).GetCouponInstanceScope(ctx, instanceID)
		if db.IsNotFound(err) {
			return errCouponNotFound
		}
		return err
	})
	if err != nil {
		return Instance{}, err
	}
	var out Instance
	err = s.writer.Write(ctx, sc.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := s.tenants.Authorize(ctx, tx, p, sc.TenantID, principal.RoleAdmin)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
			now := s.clock.Now()
			by := "user"
			row, err := q.VoidCouponInstance(ctx, sqlc.VoidCouponInstanceParams{
				Now: ts(now), VoidedByType: &by, VoidedByID: access.NullUUID(p.UserID), VoidReason: &reason,
				AppID: sc.AppID, ID: instanceID,
			})
			if err != nil {
				if db.IsNotFound(err) {
					return nil, errUnavailable
				}
				return nil, fmt.Errorf("coupon: void: %w", err)
			}
			inst, err := loadInstance(ctx, q, row, now)
			if err != nil {
				return nil, err
			}
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: sc.TenantID, AppID: &sc.AppID, Actor: p, Action: ActionVoid,
				TargetType: "member", TargetID: row.MemberID.String(),
				Metadata: map[string]any{"coupon_id": row.ID.String(), "template_id": row.TemplateID.String(), "reason": reason},
			}); err != nil {
				return nil, err
			}
			return inst, nil
		}, &out)
	return out, err
}

// loadInstance maps a coupon row with its template and redeeming store.
func loadInstance(ctx context.Context, q *sqlc.Queries, row sqlc.CouponInstance, now time.Time) (Instance, error) {
	t, err := q.GetCouponTemplate(ctx, sqlc.GetCouponTemplateParams{AppID: row.AppID, ID: row.TemplateID})
	if err != nil {
		return Instance{}, fmt.Errorf("coupon: load template: %w", err)
	}
	var storeName *string
	if row.RedeemedAtStoreID.Valid {
		st, err := q.GetStore(ctx, row.RedeemedAtStoreID.UUID)
		switch {
		case err == nil && st.AppID == row.AppID:
			storeName = &st.Name
		case err != nil && !db.IsNotFound(err):
			return Instance{}, fmt.Errorf("coupon: load store: %w", err)
		}
	}
	return instanceOf(row, summaryOf(t), storeName, now), nil
}
