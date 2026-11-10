package points

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Rules is an app's earn rule. Apps without a saved rule use DefaultRules.
type Rules struct {
	Currency        string     `json:"currency"`
	SpendPerPoint   int        `json:"spend_per_point"`
	PointsPerUnit   int        `json:"points_per_unit"`
	MinSpend        int        `json:"min_spend"`
	MaxPointsPerTxn *int       `json:"max_points_per_txn,omitempty"`
	ExpiryDays      *int       `json:"expiry_days,omitempty"`
	Enabled         bool       `json:"enabled"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}

// DefaultRules is NT$100 = 1 point, no minimum, at most 10,000 points per
// purchase, no expiry.
func DefaultRules() Rules {
	limit := DefaultMaxPointsPerTxn
	return Rules{Currency: "TWD", SpendPerPoint: 100, PointsPerUnit: 1, MaxPointsPerTxn: &limit, Enabled: true}
}

// earnCap is the most one purchase or fixed grant may earn under the rule:
// its max_points_per_txn, never above the platform ceiling.
func (r Rules) earnCap() int64 {
	if r.MaxPointsPerTxn != nil && int64(*r.MaxPointsPerTxn) < MaxEarnPerOperation {
		return int64(*r.MaxPointsPerTxn)
	}
	return MaxEarnPerOperation
}

func fromRow(r sqlc.PointsRule) Rules {
	out := Rules{
		Currency:      r.Currency,
		SpendPerPoint: int(r.SpendPerPoint),
		PointsPerUnit: int(r.PointsPerUnit),
		MinSpend:      int(r.MinSpend),
		Enabled:       r.Enabled,
	}
	if r.MaxPointsPerTxn != nil {
		v := int(*r.MaxPointsPerTxn)
		out.MaxPointsPerTxn = &v
	}
	if r.ExpiryDays != nil {
		v := int(*r.ExpiryDays)
		out.ExpiryDays = &v
	}
	if r.UpdatedAt.Valid {
		t := r.UpdatedAt.Time
		out.UpdatedAt = &t
	}
	return out
}

// rulesFor reads the app's rule inside a tenant transaction.
func rulesFor(ctx context.Context, q *sqlc.Queries, appID uuid.UUID) (Rules, error) {
	r, err := q.GetPointsRules(ctx, appID)
	if err != nil {
		if db.IsNotFound(err) {
			return DefaultRules(), nil
		}
		return Rules{}, fmt.Errorf("points: load rules: %w", err)
	}
	return fromRow(r), nil
}

// PointsFor applies the rule to a purchase: floor(spend / spend_per_point)
// units of points_per_unit points each, capped by max_points_per_txn. A
// spend below the minimum, or one that earns nothing, is a validation error.
func (r Rules) PointsFor(spend int) (int64, error) {
	if !r.Enabled {
		return 0, errDisabled
	}
	if spend < 0 || spend > MaxSpendAmount {
		return 0, apierr.Validation(map[string]string{"spend_amount": fmt.Sprintf("must be 0 to %d", MaxSpendAmount)})
	}
	if spend < r.MinSpend {
		return 0, apierr.Validation(map[string]string{"spend_amount": fmt.Sprintf("must be at least %d to earn points", r.MinSpend)})
	}
	if r.SpendPerPoint <= 0 || r.PointsPerUnit <= 0 {
		return 0, fmt.Errorf("points: invalid rule %+v", r)
	}
	// int64 throughout: spend <= 1e8 and points_per_unit <= 1e3 cannot
	// overflow, and the cap below bounds the result.
	pts := (int64(spend) / int64(r.SpendPerPoint)) * int64(r.PointsPerUnit)
	if limit := r.earnCap(); pts > limit {
		pts = limit
	}
	if pts <= 0 {
		return 0, apierr.Validation(map[string]string{"spend_amount": "earns no points under the current rule"})
	}
	if pts > MaxPointsPerOperation {
		return 0, apierr.Validation(map[string]string{"spend_amount": fmt.Sprintf("earns more than %d points", MaxPointsPerOperation)})
	}
	return pts, nil
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

func validateRules(r Rules) map[string]string {
	fields := map[string]string{}
	if !currencyCode.MatchString(r.Currency) {
		fields["currency"] = "must be a three-letter ISO 4217 code such as TWD"
	}
	if r.SpendPerPoint < 1 || r.SpendPerPoint > 1_000_000 {
		fields["spend_per_point"] = "must be 1 to 1000000"
	}
	if r.PointsPerUnit < 1 || r.PointsPerUnit > 1000 {
		fields["points_per_unit"] = "must be 1 to 1000"
	}
	if r.MinSpend < 0 || r.MinSpend > 10_000_000 {
		fields["min_spend"] = "must be 0 to 10000000"
	}
	if r.MaxPointsPerTxn != nil && (*r.MaxPointsPerTxn < 1 || *r.MaxPointsPerTxn > MaxPointsPerOperation) {
		fields["max_points_per_txn"] = "must be 1 to 1000000"
	}
	if r.ExpiryDays != nil && (*r.ExpiryDays < 1 || *r.ExpiryDays > 3650) {
		fields["expiry_days"] = "must be 1 to 3650"
	}
	return fields
}

// validateNote trims a note and checks it is 1 to 200 characters of text
// (required) or at most 200 (optional).
func validateNote(note *string, required bool) map[string]string {
	if note == nil {
		if required {
			return map[string]string{"note": "is required"}
		}
		return nil
	}
	*note = strings.TrimSpace(*note)
	n := utf8.RuneCountInString(*note)
	switch {
	case !utf8.ValidString(*note) || strings.ContainsRune(*note, 0):
		return map[string]string{"note": "must be text"}
	case required && n == 0:
		return map[string]string{"note": "is required"}
	case n > 200:
		return map[string]string{"note": "must be at most 200 characters"}
	}
	return nil
}

func validateRef(refType, refID *string) map[string]string {
	fields := map[string]string{}
	check := func(name string, v *string, max int) {
		if v == nil {
			return
		}
		*v = strings.TrimSpace(*v)
		if len(*v) > max || !utf8.ValidString(*v) || strings.ContainsRune(*v, 0) {
			fields[name] = fmt.Sprintf("must be at most %d characters of text", max)
		}
	}
	check("ref_type", refType, 32)
	check("ref_id", refID, 128)
	return fields
}

// appAccess resolves an app to its tenant and authorizes a dashboard user.
func (s *Service) appAccess(ctx context.Context, p principal.Principal, appID uuid.UUID) (uuid.UUID, error) {
	if !p.IsUser() {
		return uuid.Nil, apierr.NotFound("app")
	}
	return s.tenants.AppTenant(ctx, appID)
}

// GetRules returns the app's rule (or the defaults). Any team member may read it.
func (s *Service) GetRules(ctx context.Context, p principal.Principal, appID uuid.UUID) (Rules, error) {
	tenantID, err := s.appAccess(ctx, p, appID)
	if err != nil {
		return Rules{}, err
	}
	var out Rules
	err = db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.tenants.Authorize(ctx, tx, p, tenantID, principal.RoleStaff); err != nil {
			return err
		}
		var err error
		out, err = rulesFor(ctx, sqlc.New(tx), appID)
		return err
	})
	return out, err
}

// PutRules replaces the app's rule. Owners and admins only; audited. Points
// already earned keep the expiry they were given.
func (s *Service) PutRules(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, in Rules) (Rules, error) {
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if fields := validateRules(in); len(fields) > 0 {
		return Rules{}, apierr.Validation(fields)
	}
	tenantID, err := s.appAccess(ctx, p, appID)
	if err != nil {
		return Rules{}, err
	}
	var out Rules
	err = s.write(ctx, tenantID, scopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := s.tenants.Authorize(ctx, tx, p, tenantID, principal.RoleAdmin)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			row, err := sqlc.New(tx).UpsertPointsRules(ctx, sqlc.UpsertPointsRulesParams{
				AppID: appID, TenantID: tenantID, Currency: in.Currency,
				SpendPerPoint: int32(in.SpendPerPoint), PointsPerUnit: int32(in.PointsPerUnit),
				MinSpend: int32(in.MinSpend), MaxPointsPerTxn: int32Ptr(in.MaxPointsPerTxn),
				ExpiryDays: int32Ptr(in.ExpiryDays), Enabled: in.Enabled,
			})
			if err != nil {
				if db.Code(err) == db.CodeForeignKeyViolation {
					return nil, apierr.NotFound("app")
				}
				return nil, fmt.Errorf("points: save rules: %w", err)
			}
			saved := fromRow(row)
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: tenantID, AppID: &appID, Actor: p, Action: ActionRules,
				TargetType: "app", TargetID: appID.String(),
				Metadata: map[string]any{
					"currency": saved.Currency, "spend_per_point": saved.SpendPerPoint, "points_per_unit": saved.PointsPerUnit,
					"min_spend": saved.MinSpend, "max_points_per_txn": saved.MaxPointsPerTxn, "expiry_days": saved.ExpiryDays,
					"enabled": saved.Enabled,
				},
			}); err != nil {
				return nil, err
			}
			return saved, nil
		}, &out)
	return out, err
}

// int32Ptr narrows a validated value for an integer column. Values are
// bounded before they get here (spend <= MaxSpendAmount, rule fields by
// validateRules); anything outside int32 is a programming error.
func int32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	if *v > math.MaxInt32 || *v < math.MinInt32 {
		panic(fmt.Sprintf("points: %d does not fit an int32 column", *v))
	}
	x := int32(*v)
	return &x
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}
