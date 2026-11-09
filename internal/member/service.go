// Package member owns MINI App members: their profile, the consent they gave,
// their data rights (export and erasure) and the member lists the dashboard
// and the server API read. Members act only on themselves; dashboard users
// go through tenant membership; API keys are limited to their own app.
package member

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Audit actions written for dashboard-initiated data-rights requests.
const (
	ActionErase  = "member.erase"
	ActionExport = "member.export"
)

// Deps are the service's collaborators.
type Deps struct {
	Pool    *pgxpool.Pool
	Tenants *tenant.Service
	Clock   clock.Clock
	Log     *slog.Logger
}

// Service is the member domain.
type Service struct {
	pool    *pgxpool.Pool
	tenants *tenant.Service
	idem    *idempotency.Store
	clock   clock.Clock
	log     *slog.Logger
}

// New returns a Service. Member outcomes carry no secrets, so idempotent
// replays are stored as plain JSON.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{
		pool:    d.Pool,
		tenants: d.Tenants,
		idem:    idempotency.New(d.Pool, nil, d.Clock),
		clock:   d.Clock,
		log:     d.Log,
	}
}

// Client describes the device a member acted from, for consent records.
type Client struct {
	IP        string
	UserAgent string
}

// Idem is the client's idempotency key and the request fingerprint.
type Idem struct {
	Key  string
	Hash []byte
}

// Update is the input for Update. Nil pointers leave a field unchanged; the
// Set flags clear a field when the pointer is nil.
type Update struct {
	DisplayName  *string
	Phone        *string
	SetPhone     bool
	Birthday     *time.Time
	SetBirthday  bool
	Gender       *string
	SetGender    bool
	CustomFields map[string]any
}

// Export is everything stored about a member.
type Export struct {
	Member     sqlc.Member
	Consents   []sqlc.MemberConsent
	ExportedAt time.Time
}

// ListQuery pages through an app's members.
type ListQuery struct {
	Limit  int
	Cursor string
	Q      string
}

// Page is one page of members.
type Page struct {
	Items      []sqlc.Member
	NextCursor string
}

var errNotMember = apierr.Unauthorized("sign in with LINE")

// Me returns the calling member.
func (s *Service) Me(ctx context.Context, p principal.Principal) (sqlc.Member, error) {
	if !p.IsMember() {
		return sqlc.Member{}, errNotMember
	}
	var m sqlc.Member
	err := db.WithTenant(ctx, s.pool, p.TenantID, func(ctx context.Context, tx pgx.Tx) (err error) {
		m, err = s.ownMember(ctx, sqlc.New(tx), p)
		return err
	})
	return m, err
}

// Update changes profile fields idempotently.
func (s *Service) Update(ctx context.Context, p principal.Principal, idem Idem, in Update) (sqlc.Member, error) {
	if !p.IsMember() {
		return sqlc.Member{}, errNotMember
	}
	params, err := validateUpdate(in, s.clock.Now())
	if err != nil {
		return sqlc.Member{}, err
	}
	params.ID = p.MemberID
	return s.write(ctx, p, idem, func(ctx context.Context, q *sqlc.Queries) (sqlc.Member, error) {
		if _, err := s.ownMember(ctx, q, p); err != nil {
			return sqlc.Member{}, err
		}
		m, err := q.UpdateMemberProfile(ctx, params)
		if err != nil {
			return sqlc.Member{}, fmt.Errorf("member: update profile: %w", notFound(err))
		}
		return m, nil
	})
}

// Consent records that the member accepted the notice identified by version.
func (s *Service) Consent(ctx context.Context, p principal.Principal, idem Idem, version string, c Client) (sqlc.Member, error) {
	if !p.IsMember() {
		return sqlc.Member{}, errNotMember
	}
	if err := validateConsentVersion(version); err != nil {
		return sqlc.Member{}, err
	}
	return s.write(ctx, p, idem, func(ctx context.Context, q *sqlc.Queries) (sqlc.Member, error) {
		if _, err := s.ownMember(ctx, q, p); err != nil {
			return sqlc.Member{}, err
		}
		m, err := q.SetMemberConsent(ctx, sqlc.SetMemberConsentParams{ID: p.MemberID, ConsentVersion: &version})
		if err != nil {
			return sqlc.Member{}, fmt.Errorf("member: set consent: %w", notFound(err))
		}
		if err := q.InsertMemberConsent(ctx, sqlc.InsertMemberConsentParams{
			TenantID: m.TenantID, AppID: m.AppID, MemberID: m.ID, Version: version,
			Ip: parseIP(c.IP), UserAgent: nonEmpty(c.UserAgent),
		}); err != nil {
			return sqlc.Member{}, fmt.Errorf("member: insert consent: %w", err)
		}
		return m, nil
	})
}

// ExportSelf is the member's own access request.
func (s *Service) ExportSelf(ctx context.Context, p principal.Principal) (Export, error) {
	if !p.IsMember() {
		return Export{}, errNotMember
	}
	var out Export
	err := db.WithTenant(ctx, s.pool, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		m, err := s.ownMember(ctx, q, p)
		if err != nil {
			return err
		}
		out, err = s.export(ctx, q, m)
		return err
	})
	return out, err
}

// EraseSelf is the member's own erasure request. The session the member used
// stops authenticating because the row is marked anonymized.
func (s *Service) EraseSelf(ctx context.Context, p principal.Principal) error {
	if !p.IsMember() {
		return errNotMember
	}
	return db.WithTenant(ctx, s.pool, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := s.ownMember(ctx, q, p); err != nil {
			return err
		}
		n, err := q.AnonymizeMember(ctx, p.MemberID)
		if err != nil {
			return fmt.Errorf("member: anonymize: %w", err)
		}
		if n != 1 {
			return apierr.NotFound("member")
		}
		s.log.InfoContext(ctx, "member erased their data", "member_id", p.MemberID, "app_id", p.AppID)
		return nil
	})
}

// List pages through an app's members for a dashboard user (any role) or
// the app's own API key.
func (s *Service) List(ctx context.Context, p principal.Principal, appID uuid.UUID, in ListQuery) (Page, error) {
	limit, cursor, err := parseListQuery(in)
	if err != nil {
		return Page{}, err
	}
	tenantID, err := s.appScope(ctx, p, appID)
	if err != nil {
		return Page{}, err
	}
	var page Page
	err = db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		if p.IsUser() {
			if _, err := s.tenants.Authorize(ctx, tx, p, tenantID, principal.RoleStaff); err != nil {
				return err
			}
		}
		params := sqlc.ListMembersParams{AppID: appID, PageLimit: int32(limit + 1)}
		if in.Q != "" {
			q := in.Q
			params.Q = &q
		}
		if cursor != nil {
			params.BeforeCreatedAt = pgtype.Timestamptz{Time: cursor.createdAt, Valid: true}
			params.BeforeID = uuid.NullUUID{UUID: cursor.id, Valid: true}
		}
		rows, err := q.ListMembers(ctx, params)
		if err != nil {
			return fmt.Errorf("member: list: %w", err)
		}
		if len(rows) > limit {
			last := rows[limit-1]
			page.NextCursor = encodeCursor(last.CreatedAt.Time, last.ID)
			rows = rows[:limit]
		}
		page.Items = rows
		return nil
	})
	return page, err
}

// Get returns one member for a dashboard user or the app's API key.
func (s *Service) Get(ctx context.Context, p principal.Principal, memberID uuid.UUID) (sqlc.Member, error) {
	var m sqlc.Member
	err := s.readMember(ctx, p, memberID, principal.RoleStaff, func(_ context.Context, _ pgx.Tx, found sqlc.Member) error {
		m = found
		return nil
	})
	return m, err
}

// Export handles an access request on the member's behalf. Owners and
// admins only; audited.
func (s *Service) Export(ctx context.Context, p principal.Principal, memberID uuid.UUID) (Export, error) {
	var out Export
	err := s.readMember(ctx, p, memberID, principal.RoleAdmin, func(ctx context.Context, tx pgx.Tx, m sqlc.Member) error {
		var err error
		out, err = s.export(ctx, sqlc.New(tx), m)
		if err != nil {
			return err
		}
		if p.IsUser() {
			return s.audit(ctx, tx, p, m, ActionExport)
		}
		return nil
	})
	return out, err
}

// Erase handles an erasure request on the member's behalf. Owners and
// admins only; audited.
func (s *Service) Erase(ctx context.Context, p principal.Principal, memberID uuid.UUID) error {
	if !p.IsUser() {
		return apierr.NotFound("member")
	}
	return s.readMember(ctx, p, memberID, principal.RoleAdmin, func(ctx context.Context, tx pgx.Tx, m sqlc.Member) error {
		n, err := sqlc.New(tx).AnonymizeMember(ctx, m.ID)
		if err != nil {
			return fmt.Errorf("member: anonymize: %w", err)
		}
		if n != 1 {
			return apierr.NotFound("member")
		}
		return s.audit(ctx, tx, p, m, ActionErase)
	})
}

// readMember resolves the member's tenant, authorizes the caller and runs fn
// with the member inside the tenant transaction. API keys may only reach
// members of their own app.
func (s *Service) readMember(ctx context.Context, p principal.Principal, memberID uuid.UUID, min principal.Role, fn func(ctx context.Context, tx pgx.Tx, m sqlc.Member) error) error {
	if !p.IsUser() && !p.IsAPIKey() {
		return apierr.NotFound("member")
	}
	var tenantID uuid.UUID
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		id, err := sqlc.New(tx).GetMemberTenant(ctx, memberID)
		if err != nil {
			return notFound(err)
		}
		tenantID = id
		return nil
	})
	if err != nil {
		return err
	}
	if p.IsAPIKey() && p.TenantID != tenantID {
		return apierr.NotFound("member")
	}
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		if p.IsUser() {
			if _, err := s.tenants.Authorize(ctx, tx, p, tenantID, min); err != nil {
				return err
			}
		}
		m, err := q.GetMember(ctx, memberID)
		if err != nil {
			return notFound(err)
		}
		if p.IsAPIKey() && m.AppID != p.AppID {
			return apierr.NotFound("member")
		}
		if m.AnonymizedAt.Valid {
			return apierr.NotFound("member")
		}
		return fn(ctx, tx, m)
	})
}

// appScope resolves the tenant of appID for the caller: dashboard users
// through the app lookup, API keys only for their own app.
func (s *Service) appScope(ctx context.Context, p principal.Principal, appID uuid.UUID) (uuid.UUID, error) {
	switch {
	case p.IsAPIKey():
		if p.AppID != appID {
			return uuid.Nil, apierr.NotFound("app")
		}
		return p.TenantID, nil
	case p.IsUser():
		return s.tenants.AppTenant(ctx, appID)
	default:
		return uuid.Nil, apierr.NotFound("app")
	}
}

// ownMember loads the calling member and refuses erased rows.
func (s *Service) ownMember(ctx context.Context, q *sqlc.Queries, p principal.Principal) (sqlc.Member, error) {
	m, err := q.GetMember(ctx, p.MemberID)
	if err != nil {
		if db.IsNotFound(err) {
			return sqlc.Member{}, errNotMember
		}
		return sqlc.Member{}, fmt.Errorf("member: load: %w", err)
	}
	if m.AnonymizedAt.Valid || m.AppID != p.AppID {
		return sqlc.Member{}, errNotMember
	}
	return m, nil
}

func (s *Service) export(ctx context.Context, q *sqlc.Queries, m sqlc.Member) (Export, error) {
	consents, err := q.ListMemberConsents(ctx, m.ID)
	if err != nil {
		return Export{}, fmt.Errorf("member: list consents: %w", err)
	}
	return Export{Member: m, Consents: consents, ExportedAt: s.clock.Now()}, nil
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, p principal.Principal, m sqlc.Member, action string) error {
	appID := m.AppID
	return audit.Record(ctx, tx, audit.Entry{
		TenantID:   m.TenantID,
		AppID:      &appID,
		Actor:      p,
		Action:     action,
		TargetType: "member",
		TargetID:   m.ID.String(),
	})
}

// write runs fn once per idempotency key inside the member's tenant
// transaction and replays the stored member on retries.
func (s *Service) write(ctx context.Context, p principal.Principal, idem Idem, fn func(ctx context.Context, q *sqlc.Queries) (sqlc.Member, error)) (sqlc.Member, error) {
	var fresh sqlc.Member
	res, err := s.idem.Run(ctx, idempotency.Request{
		TenantID: p.TenantID,
		Scope:    idempotency.Scope("member", p.MemberID),
		Key:      idem.Key,
		Hash:     idem.Hash,
	}, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		m, err := fn(ctx, sqlc.New(tx))
		if err != nil {
			return 0, nil, err
		}
		fresh = m
		return 200, m, nil
	})
	if err != nil {
		return sqlc.Member{}, err
	}
	if !res.Replayed {
		return fresh, nil
	}
	var out sqlc.Member
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return sqlc.Member{}, fmt.Errorf("member: decode replay: %w", err)
	}
	return out, nil
}

func notFound(err error) error {
	if db.IsNotFound(err) {
		return apierr.NotFound("member")
	}
	return err
}

func parseIP(s string) *netip.Addr {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &addr
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
