package coupon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
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

// TemplateInput creates a template.
type TemplateInput struct {
	Name           string
	Description    *string
	Type           string
	Value          *int
	TotalLimit     *int
	PerMemberLimit *int
	ValidFrom      *time.Time
	ValidTo        *time.Time
	ValidityDays   *int
	Claimable      *bool
}

// TemplateUpdate changes a template. Nil fields keep their value; Clear
// names fields to set to null.
type TemplateUpdate struct {
	Name           *string
	Description    *string
	Type           *string
	Value          *int
	TotalLimit     *int
	PerMemberLimit *int
	ValidFrom      *time.Time
	ValidTo        *time.Time
	ValidityDays   *int
	Claimable      *bool
	Status         *string
	Clear          []string
}

// Clearable template fields.
var clearable = []string{"description", "value", "total_limit", "valid_from", "valid_to", "validity_days"}

// draft is a template's editable state, validated as a whole on create and
// after merging an update.
type draft struct {
	name           string
	description    *string
	typ            string
	value          *int
	totalLimit     *int
	perMemberLimit int
	validFrom      *time.Time
	validTo        *time.Time
	validityDays   *int
	claimable      bool
	status         string
}

func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// validate checks the draft against the create rules.
func (d draft) validate() map[string]string {
	fields := map[string]string{}
	if n := utf8.RuneCountInString(d.name); n < 1 || n > 100 || !validText(d.name) {
		fields["name"] = "must be 1 to 100 characters of text"
	}
	if d.description != nil && (utf8.RuneCountInString(*d.description) > 500 || !validText(*d.description)) {
		fields["description"] = "must be at most 500 characters of text"
	}
	switch d.typ {
	case TypeAmountOff:
		if d.value == nil || *d.value < 1 || *d.value > 1_000_000 {
			fields["value"] = "amount_off takes 1 to 1000000 currency units"
		}
	case TypePercentOff:
		if d.value == nil || *d.value < 1 || *d.value > 100 {
			fields["value"] = "percent_off takes 1 to 100 percent"
		}
	case TypeFreeItem:
		if d.value != nil {
			fields["value"] = "free_item takes no value; name the item instead"
		}
	default:
		fields["type"] = "must be amount_off, percent_off or free_item"
	}
	if d.totalLimit != nil && (*d.totalLimit < 1 || *d.totalLimit > 10_000_000) {
		fields["total_limit"] = "must be 1 to 10000000"
	}
	if d.perMemberLimit < 1 || d.perMemberLimit > 100 {
		fields["per_member_limit"] = "must be 1 to 100"
	} else if d.totalLimit != nil && d.perMemberLimit > *d.totalLimit {
		fields["per_member_limit"] = "must not exceed total_limit"
	}
	if d.validFrom != nil && d.validTo != nil && !d.validTo.After(*d.validFrom) {
		fields["valid_to"] = "must be after valid_from"
	}
	if d.validityDays != nil && (*d.validityDays < 1 || *d.validityDays > 3650) {
		fields["validity_days"] = "must be 1 to 3650"
	}
	if d.status != "active" && d.status != "archived" {
		fields["status"] = "must be active or archived"
	}
	return fields
}

func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

// templateTenant resolves a template to its tenant and app with a system
// lookup; the caller then authorizes inside the tenant transaction.
func (s *Service) templateTenant(ctx context.Context, templateID uuid.UUID) (sqlc.GetCouponTemplateScopeRow, error) {
	var out sqlc.GetCouponTemplateScopeRow
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		out, err = sqlc.New(tx).GetCouponTemplateScope(ctx, templateID)
		if db.IsNotFound(err) {
			return errTemplateNotFound
		}
		return err
	})
	return out, err
}

// appTenant resolves an app for a dashboard user.
func (s *Service) appTenant(ctx context.Context, p principal.Principal, appID uuid.UUID) (uuid.UUID, error) {
	if !p.IsUser() {
		return uuid.Nil, apierr.NotFound("app")
	}
	return s.tenants.AppTenant(ctx, appID)
}

// ListTemplates lists an app's templates, newest first. Any team member.
func (s *Service) ListTemplates(ctx context.Context, p principal.Principal, appID uuid.UUID, status *string) ([]Template, error) {
	if status != nil && *status != "active" && *status != "archived" {
		return nil, apierr.BadRequest("status must be active or archived")
	}
	tenantID, err := s.appTenant(ctx, p, appID)
	if err != nil {
		return nil, err
	}
	out := []Template{}
	err = db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.tenants.Authorize(ctx, tx, p, tenantID, principal.RoleStaff); err != nil {
			return err
		}
		rows, err := sqlc.New(tx).ListCouponTemplates(ctx, sqlc.ListCouponTemplatesParams{AppID: appID, Status: status, PageLimit: maxTemplates})
		if err != nil {
			return fmt.Errorf("coupon: list templates: %w", err)
		}
		for _, r := range rows {
			out = append(out, toTemplate(r))
		}
		return nil
	})
	return out, err
}

// GetTemplate returns one template. Any team member.
func (s *Service) GetTemplate(ctx context.Context, p principal.Principal, templateID uuid.UUID) (Template, error) {
	if !p.IsUser() {
		return Template{}, errTemplateNotFound
	}
	sc, err := s.templateTenant(ctx, templateID)
	if err != nil {
		return Template{}, err
	}
	var out Template
	err = db.WithTenant(ctx, s.pool, sc.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.tenants.Authorize(ctx, tx, p, sc.TenantID, principal.RoleStaff); err != nil {
			return err
		}
		t, err := sqlc.New(tx).GetCouponTemplate(ctx, sqlc.GetCouponTemplateParams{AppID: sc.AppID, ID: templateID})
		if err != nil {
			if db.IsNotFound(err) {
				return errTemplateNotFound
			}
			return fmt.Errorf("coupon: get template: %w", err)
		}
		out = toTemplate(t)
		return nil
	})
	return out, err
}

// CreateTemplate adds a template to an app. Owners and admins; audited.
func (s *Service) CreateTemplate(ctx context.Context, p principal.Principal, appID uuid.UUID, idem Idem, in TemplateInput) (Template, error) {
	d := draft{
		name: strings.TrimSpace(in.Name), description: trimmed(in.Description), typ: in.Type, value: in.Value,
		totalLimit: in.TotalLimit, perMemberLimit: 1, validFrom: in.ValidFrom, validTo: in.ValidTo,
		validityDays: in.ValidityDays, status: "active",
	}
	if in.PerMemberLimit != nil {
		d.perMemberLimit = *in.PerMemberLimit
	}
	if in.Claimable != nil {
		d.claimable = *in.Claimable
	}
	if fields := d.validate(); len(fields) > 0 {
		return Template{}, apierr.Validation(fields)
	}
	tenantID, err := s.appTenant(ctx, p, appID)
	if err != nil {
		return Template{}, err
	}
	var out Template
	err = s.writer.Write(ctx, tenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := s.tenants.Authorize(ctx, tx, p, tenantID, principal.RoleAdmin)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			row, err := sqlc.New(tx).CreateCouponTemplate(ctx, sqlc.CreateCouponTemplateParams{
				ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, AppID: appID, Name: d.name, Description: d.description,
				Type: d.typ, Value: int32Of(d.value), TotalLimit: int32Of(d.totalLimit), PerMemberLimit: int32(d.perMemberLimit),
				ValidFrom: tsOf(d.validFrom), ValidTo: tsOf(d.validTo), ValidityDays: int32Of(d.validityDays),
				Claimable: d.claimable, Now: ts(s.clock.Now()),
			})
			if err != nil {
				if db.Code(err) == db.CodeForeignKeyViolation {
					return nil, apierr.NotFound("app")
				}
				return nil, fmt.Errorf("coupon: create template: %w", err)
			}
			t := toTemplate(row)
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: tenantID, AppID: &appID, Actor: p, Action: ActionTemplateCreate,
				TargetType: "coupon_template", TargetID: t.ID.String(), Metadata: templateAudit(t),
			}); err != nil {
				return nil, err
			}
			return t, nil
		}, &out)
	return out, err
}

// UpdateTemplate changes or archives a template. Owners and admins;
// audited. Type and value are fixed once a coupon was issued from it, and
// total_limit never drops below claimed_count; both are checked under the
// template lock, so a concurrent claim cannot slip in between.
func (s *Service) UpdateTemplate(ctx context.Context, p principal.Principal, templateID uuid.UUID, idem Idem, in TemplateUpdate) (Template, error) {
	if !p.IsUser() {
		return Template{}, errTemplateNotFound
	}
	cleared := map[string]bool{}
	fields := map[string]string{}
	for _, c := range in.Clear {
		if !slices.Contains(clearable, c) {
			fields["clear"] = "can clear only " + strings.Join(clearable, ", ")
			continue
		}
		cleared[c] = true
	}
	sent := map[string]bool{
		"description": in.Description != nil, "value": in.Value != nil, "total_limit": in.TotalLimit != nil,
		"valid_from": in.ValidFrom != nil, "valid_to": in.ValidTo != nil, "validity_days": in.ValidityDays != nil,
	}
	for c := range cleared {
		if sent[c] {
			fields[c] = "send it or clear it, not both"
		}
	}
	if len(fields) > 0 {
		return Template{}, apierr.Validation(fields)
	}
	sc, err := s.templateTenant(ctx, templateID)
	if err != nil {
		return Template{}, err
	}
	var out Template
	err = s.writer.Write(ctx, sc.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := s.tenants.Authorize(ctx, tx, p, sc.TenantID, principal.RoleAdmin)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
			cur, err := lockTemplate(ctx, q, sc.AppID, templateID)
			if err != nil {
				return nil, err
			}
			d, params := merge(cur, in, cleared)
			if cur.ClaimedCount > 0 && (d.typ != cur.Type || !sameInt(d.value, intOf(cur.Value))) {
				return nil, errInUse
			}
			if d.totalLimit != nil && *d.totalLimit < int(cur.ClaimedCount) {
				return nil, apierr.New(422, "validation_failed", "request is invalid").WithDetails(map[string]any{
					"total_limit": fmt.Sprintf("must be at least %d, the coupons already issued", cur.ClaimedCount),
				})
			}
			if fields := d.validate(); len(fields) > 0 {
				return nil, apierr.Validation(fields)
			}
			params.Now = ts(s.clock.Now())
			row, err := q.UpdateCouponTemplate(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("coupon: update template: %w", err)
			}
			t := toTemplate(row)
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: sc.TenantID, AppID: &sc.AppID, Actor: p, Action: ActionTemplateUpdate,
				TargetType: "coupon_template", TargetID: t.ID.String(), Metadata: templateAudit(t),
			}); err != nil {
				return nil, err
			}
			return t, nil
		}, &out)
	return out, err
}

// merge applies an update to the current row: the resulting draft to
// validate, and the query parameters that write it. claimable is always
// written explicitly.
func merge(cur sqlc.CouponTemplate, in TemplateUpdate, cleared map[string]bool) (draft, sqlc.UpdateCouponTemplateParams) {
	d := draft{
		name: cur.Name, description: cur.Description, typ: cur.Type, value: intOf(cur.Value),
		totalLimit: intOf(cur.TotalLimit), perMemberLimit: int(cur.PerMemberLimit), validFrom: timeOf(cur.ValidFrom),
		validTo: timeOf(cur.ValidTo), validityDays: intOf(cur.ValidityDays), claimable: cur.Claimable, status: cur.Status,
	}
	params := sqlc.UpdateCouponTemplateParams{AppID: cur.AppID, ID: cur.ID}
	if in.Name != nil {
		d.name = strings.TrimSpace(*in.Name)
		params.Name = &d.name
	}
	switch {
	case in.Description != nil:
		d.description = trimmed(in.Description)
		params.SetDescription, params.Description = true, d.description
	case cleared["description"]:
		d.description = nil
		params.SetDescription = true
	}
	if in.Type != nil {
		d.typ = *in.Type
		params.Type = in.Type
	}
	switch {
	case in.Value != nil:
		d.value = in.Value
		params.SetValue, params.Value = true, int32Of(in.Value)
	case cleared["value"], in.Type != nil && *in.Type == TypeFreeItem:
		// Switching to free_item drops the old value, as the type demands.
		d.value = nil
		params.SetValue = true
	}
	switch {
	case in.TotalLimit != nil:
		d.totalLimit = in.TotalLimit
		params.SetTotalLimit, params.TotalLimit = true, int32Of(in.TotalLimit)
	case cleared["total_limit"]:
		d.totalLimit = nil
		params.SetTotalLimit = true
	}
	if in.PerMemberLimit != nil {
		d.perMemberLimit = *in.PerMemberLimit
		params.PerMemberLimit = int32Of(in.PerMemberLimit)
	}
	switch {
	case in.ValidFrom != nil:
		d.validFrom = in.ValidFrom
		params.SetValidFrom, params.ValidFrom = true, tsOf(in.ValidFrom)
	case cleared["valid_from"]:
		d.validFrom = nil
		params.SetValidFrom = true
	}
	switch {
	case in.ValidTo != nil:
		d.validTo = in.ValidTo
		params.SetValidTo, params.ValidTo = true, tsOf(in.ValidTo)
	case cleared["valid_to"]:
		d.validTo = nil
		params.SetValidTo = true
	}
	switch {
	case in.ValidityDays != nil:
		d.validityDays = in.ValidityDays
		params.SetValidityDays, params.ValidityDays = true, int32Of(in.ValidityDays)
	case cleared["validity_days"]:
		d.validityDays = nil
		params.SetValidityDays = true
	}
	if in.Claimable != nil {
		d.claimable = *in.Claimable
	}
	params.Claimable = &d.claimable
	if in.Status != nil {
		d.status = *in.Status
		params.Status = in.Status
	}
	return d, params
}

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// templateAudit is the audit metadata of a template: its settings, not the
// member-facing text.
func templateAudit(t Template) map[string]any {
	return map[string]any{
		"name": t.Name, "type": t.Type, "value": t.Value, "total_limit": t.TotalLimit,
		"per_member_limit": t.PerMemberLimit, "valid_from": t.ValidFrom, "valid_to": t.ValidTo,
		"validity_days": t.ValidityDays, "claimable": t.Claimable, "status": t.Status,
	}
}
