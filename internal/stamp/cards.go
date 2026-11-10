package stamp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// CardInput creates a stamp card.
type CardInput struct {
	Name                   string
	StampsRequired         int
	RewardCouponTemplateID *uuid.UUID
	MaxStampsPerVisit      *int
}

// CardUpdate changes a stamp card. Nil fields keep their value;
// ClearReward removes the reward coupon.
type CardUpdate struct {
	Name                   *string
	StampsRequired         *int
	RewardCouponTemplateID *uuid.UUID
	MaxStampsPerVisit      *int
	Status                 *string
	ClearReward            bool
}

func validateCard(name *string, required, perVisit *int, status *string) map[string]string {
	fields := map[string]string{}
	if name != nil {
		if n := utf8.RuneCountInString(*name); n < 1 || n > 100 || !utf8.ValidString(*name) || strings.ContainsRune(*name, 0) {
			fields["name"] = "must be 1 to 100 characters of text"
		}
	}
	if required != nil && (*required < 2 || *required > 100) {
		fields["stamps_required"] = "must be 2 to 100"
	}
	if perVisit != nil && (*perVisit < 1 || *perVisit > 100) {
		fields["max_stamps_per_visit"] = "must be 1 to 100"
	}
	if status != nil && *status != "active" && *status != "archived" {
		fields["status"] = "must be active or archived"
	}
	return fields
}

// checkReward makes sure the reward template is one of the app's own.
func checkReward(ctx context.Context, q *sqlc.Queries, appID uuid.UUID, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	if _, err := q.GetCouponTemplate(ctx, sqlc.GetCouponTemplateParams{AppID: appID, ID: *id}); err != nil {
		if db.IsNotFound(err) {
			return errTemplateNotFound
		}
		return fmt.Errorf("stamp: load reward template: %w", err)
	}
	return nil
}

func (s *Service) appTenant(ctx context.Context, p principal.Principal, appID uuid.UUID) (uuid.UUID, error) {
	if !p.IsUser() {
		return uuid.Nil, apierr.NotFound("app")
	}
	return s.tenants.AppTenant(ctx, appID)
}

// cardTenant resolves a card to its tenant and app with a system lookup.
func (s *Service) cardTenant(ctx context.Context, cardID uuid.UUID) (sqlc.GetStampCardScopeRow, error) {
	var out sqlc.GetStampCardScopeRow
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		out, err = sqlc.New(tx).GetStampCardScope(ctx, cardID)
		if db.IsNotFound(err) {
			return errCardNotFound
		}
		return err
	})
	return out, err
}

// ListCards lists an app's stamp cards, newest first. Any team member.
func (s *Service) ListCards(ctx context.Context, p principal.Principal, appID uuid.UUID, status *string) ([]Card, error) {
	if status != nil && *status != "active" && *status != "archived" {
		return nil, apierr.BadRequest("status must be active or archived")
	}
	tenantID, err := s.appTenant(ctx, p, appID)
	if err != nil {
		return nil, err
	}
	out := []Card{}
	err = db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.tenants.Authorize(ctx, tx, p, tenantID, principal.RoleStaff); err != nil {
			return err
		}
		rows, err := sqlc.New(tx).ListStampCards(ctx, sqlc.ListStampCardsParams{AppID: appID, Status: status, PageLimit: maxCards})
		if err != nil {
			return fmt.Errorf("stamp: list cards: %w", err)
		}
		for _, r := range rows {
			out = append(out, toCard(r))
		}
		return nil
	})
	return out, err
}

// GetCard returns one stamp card. Any team member.
func (s *Service) GetCard(ctx context.Context, p principal.Principal, cardID uuid.UUID) (Card, error) {
	if !p.IsUser() {
		return Card{}, errCardNotFound
	}
	sc, err := s.cardTenant(ctx, cardID)
	if err != nil {
		return Card{}, err
	}
	var out Card
	err = db.WithTenant(ctx, s.pool, sc.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.tenants.Authorize(ctx, tx, p, sc.TenantID, principal.RoleStaff); err != nil {
			return err
		}
		c, err := sqlc.New(tx).GetStampCard(ctx, sqlc.GetStampCardParams{AppID: sc.AppID, ID: cardID})
		if err != nil {
			if db.IsNotFound(err) {
				return errCardNotFound
			}
			return fmt.Errorf("stamp: get card: %w", err)
		}
		out = toCard(c)
		return nil
	})
	return out, err
}

// CreateCard adds a stamp card to an app. Owners and admins; audited.
func (s *Service) CreateCard(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, in CardInput) (Card, error) {
	name := strings.TrimSpace(in.Name)
	perVisit := 1
	if in.MaxStampsPerVisit != nil {
		perVisit = *in.MaxStampsPerVisit
	}
	if fields := validateCard(&name, &in.StampsRequired, &perVisit, nil); len(fields) > 0 {
		return Card{}, apierr.Validation(fields)
	}
	tenantID, err := s.appTenant(ctx, p, appID)
	if err != nil {
		return Card{}, err
	}
	var out Card
	err = s.writer.Write(ctx, tenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := s.tenants.Authorize(ctx, tx, p, tenantID, principal.RoleAdmin)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
			if err := checkReward(ctx, q, appID, in.RewardCouponTemplateID); err != nil {
				return nil, err
			}
			var reward uuid.NullUUID
			if in.RewardCouponTemplateID != nil {
				reward = uuid.NullUUID{UUID: *in.RewardCouponTemplateID, Valid: true}
			}
			row, err := q.CreateStampCard(ctx, sqlc.CreateStampCardParams{
				ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, AppID: appID, Name: name,
				StampsRequired: int32(in.StampsRequired), RewardCouponTemplateID: reward,
				MaxStampsPerVisit: int32(perVisit), Now: ts(s.clock.Now()),
			})
			if err != nil {
				if db.Code(err) == db.CodeForeignKeyViolation {
					return nil, apierr.NotFound("app")
				}
				return nil, fmt.Errorf("stamp: create card: %w", err)
			}
			c := toCard(row)
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: tenantID, AppID: &appID, Actor: p, Action: ActionCardCreate,
				TargetType: "stamp_card", TargetID: c.ID.String(), Metadata: cardAudit(c),
			}); err != nil {
				return nil, err
			}
			return c, nil
		}, &out)
	return out, err
}

// UpdateCard changes or archives a stamp card. Owners and admins; audited.
// stamps_required is fixed once any member has a stamp on the card; the
// check runs under the card's row lock, which stamps hold in share mode, so
// a first stamp cannot race it.
func (s *Service) UpdateCard(ctx context.Context, p principal.Principal, cardID uuid.UUID, idem Idem, in CardUpdate) (Card, error) {
	if !p.IsUser() {
		return Card{}, errCardNotFound
	}
	var name *string
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		name = &n
	}
	fields := validateCard(name, in.StampsRequired, in.MaxStampsPerVisit, in.Status)
	if in.ClearReward && in.RewardCouponTemplateID != nil {
		fields["reward_coupon_template_id"] = "send it or clear it, not both"
	}
	if len(fields) > 0 {
		return Card{}, apierr.Validation(fields)
	}
	sc, err := s.cardTenant(ctx, cardID)
	if err != nil {
		return Card{}, err
	}
	var out Card
	err = s.writer.Write(ctx, sc.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := s.tenants.Authorize(ctx, tx, p, sc.TenantID, principal.RoleAdmin)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
			cur, err := q.LockStampCard(ctx, sqlc.LockStampCardParams{AppID: sc.AppID, ID: cardID})
			if err != nil {
				if db.IsNotFound(err) {
					return nil, errCardNotFound
				}
				return nil, fmt.Errorf("stamp: lock card: %w", err)
			}
			params := sqlc.UpdateStampCardParams{AppID: sc.AppID, ID: cardID, Name: name, Status: in.Status, Now: ts(s.clock.Now())}
			if in.StampsRequired != nil && int32(*in.StampsRequired) != cur.StampsRequired {
				inUse, err := q.StampCardInUse(ctx, sqlc.StampCardInUseParams{AppID: sc.AppID, StampCardID: cardID})
				if err != nil {
					return nil, fmt.Errorf("stamp: card in use: %w", err)
				}
				if inUse {
					return nil, errInUse
				}
				v := int32(*in.StampsRequired)
				params.StampsRequired = &v
			}
			if in.MaxStampsPerVisit != nil {
				v := int32(*in.MaxStampsPerVisit)
				params.MaxStampsPerVisit = &v
			}
			switch {
			case in.RewardCouponTemplateID != nil:
				if err := checkReward(ctx, q, sc.AppID, in.RewardCouponTemplateID); err != nil {
					return nil, err
				}
				params.SetRewardCouponTemplateID = true
				params.RewardCouponTemplateID = uuid.NullUUID{UUID: *in.RewardCouponTemplateID, Valid: true}
			case in.ClearReward:
				params.SetRewardCouponTemplateID = true
			}
			row, err := q.UpdateStampCard(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("stamp: update card: %w", err)
			}
			c := toCard(row)
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: sc.TenantID, AppID: &sc.AppID, Actor: p, Action: ActionCardUpdate,
				TargetType: "stamp_card", TargetID: c.ID.String(), Metadata: cardAudit(c),
			}); err != nil {
				return nil, err
			}
			return c, nil
		}, &out)
	return out, err
}

func cardAudit(c Card) map[string]any {
	return map[string]any{
		"name": c.Name, "stamps_required": c.StampsRequired, "reward_coupon_template_id": c.RewardCouponTemplateID,
		"max_stamps_per_visit": c.MaxStampsPerVisit, "status": c.Status,
	}
}
