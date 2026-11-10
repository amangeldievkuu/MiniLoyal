// Package staffauth signs store staff in to the staff app (a LIFF app) and
// serves what the staff app needs around the member card: the staff
// member's own profile and scanning a member's rotating QR code.
//
// A staff member is created in the dashboard with a one-time invite code.
// The first sign-in sends a LIFF ID token plus that code, which binds the
// staff record to the LINE account; later sign-ins need only the token.
// Codes are matched within the app only and attempts are rate limited.
package staffauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Defaults.
const (
	DefaultIssuer          = "miniloyal"
	DefaultSessionTTL      = 12 * time.Hour // one shift
	defaultIPPerMinute     = 20
	defaultInvitePerMinute = 5
	minSigningKeyLen       = 32

	minIDTokenLen    = 20
	maxIDTokenLen    = 4096
	maxInviteCodeLen = 32
)

// Config tunes the service. Zero values take the defaults.
type Config struct {
	SigningKey      []byte
	Issuer          string
	SessionTTL      time.Duration
	IPPerMinute     int
	InvitePerMinute int
}

// Deps are the service's collaborators.
type Deps struct {
	Pool *pgxpool.Pool
	// LineAuth verifies LIFF ID tokens against the app's channels.
	LineAuth *lineauth.Service
	// Card verifies member QR codes and issues scan tokens.
	Card   *card.Codec
	Clock  clock.Clock
	Log    *slog.Logger
	Config Config
}

// Service is the staff app service.
type Service struct {
	pool        *pgxpool.Pool
	lineauth    *lineauth.Service
	card        *card.Codec
	clock       clock.Clock
	log         *slog.Logger
	signer      *signer
	sessionTTL  time.Duration
	ipLimit     *auth.Limiter
	inviteLimit *auth.Limiter
	// inviteKey is the invite-code HMAC key, derived like the tenant
	// service's from the session signing key.
	inviteKey []byte
}

// New builds a Service.
func New(d Deps) (*Service, error) {
	if d.Pool == nil {
		return nil, errors.New("staffauth: a database pool is required")
	}
	if d.LineAuth == nil {
		return nil, errors.New("staffauth: the LINE sign-in service is required")
	}
	if d.Card == nil {
		return nil, errors.New("staffauth: the card codec is required")
	}
	if len(d.Config.SigningKey) < minSigningKeyLen {
		return nil, fmt.Errorf("staffauth: signing key must be at least %d bytes", minSigningKeyLen)
	}
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	cfg := d.Config
	if cfg.Issuer == "" {
		cfg.Issuer = DefaultIssuer
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.IPPerMinute == 0 {
		cfg.IPPerMinute = defaultIPPerMinute
	}
	if cfg.InvitePerMinute == 0 {
		cfg.InvitePerMinute = defaultInvitePerMinute
	}
	return &Service{
		pool:        d.Pool,
		lineauth:    d.LineAuth,
		card:        d.Card,
		clock:       d.Clock,
		log:         d.Log,
		signer:      &signer{key: cfg.SigningKey, issuer: cfg.Issuer, clock: d.Clock},
		sessionTTL:  cfg.SessionTTL,
		ipLimit:     auth.NewLimiter(d.Clock, cfg.IPPerMinute, cfg.IPPerMinute),
		inviteLimit: auth.NewLimiter(d.Clock, cfg.InvitePerMinute, cfg.InvitePerMinute),
		inviteKey:   tenant.InviteKey(cfg.SigningKey),
	}, nil
}

// Client describes the device signing in.
type Client struct {
	IP        string
	UserAgent string
}

// LoginInput is the staff app's sign-in request.
type LoginInput struct {
	AppID      uuid.UUID
	IDToken    string
	InviteCode string
}

// Staff is a staff member with the name of their store, if any.
type Staff struct {
	sqlc.Staff
	StoreName *string
}

// Session is a signed-in staff member.
type Session struct {
	Token     string
	ExpiresIn int
	Staff     Staff
	App       sqlc.App
}

// ScanResult is a verified member card.
type ScanResult struct {
	Member    sqlc.Member
	Balance   int64
	ScanToken string
	ExpiresAt time.Time
}

var (
	// errStaffLogin is the one answer for an unknown LINE account, a wrong,
	// expired, reused or other-app code, and a disabled staff member.
	errStaffLogin  = apierr.Unauthorized("this LINE account is not a staff member of the app; check the invite code")
	errBadSession  = apierr.Unauthorized("invalid or expired staff session")
	errNotStaff    = apierr.Unauthorized("sign in to the staff app")
	errBadCard     = apierr.New(422, "invalid_card", "this is not a valid member card for this app")
	errCardExpired = apierr.New(422, "card_expired", "this card code has expired; ask the member to show the card again")
)

// LoginWithLine verifies the LIFF ID token for the app, binds the LINE
// account with the invite code on the first sign-in, and returns a session.
func (s *Service) LoginWithLine(ctx context.Context, in LoginInput, c Client) (*Session, error) {
	ip := c.IP
	if ip == "" {
		ip = "unknown"
	}
	if wait, ok := s.ipLimit.Allow("ip:" + ip); !ok {
		return nil, apierr.RateLimited(wait)
	}
	in.IDToken = strings.TrimSpace(in.IDToken)
	code := tenant.NormalizeInviteCode(in.InviteCode)
	fields := map[string]string{}
	if in.AppID == uuid.Nil {
		fields["app_id"] = "is required"
	}
	if n := len(in.IDToken); n < minIDTokenLen || n > maxIDTokenLen {
		fields["id_token"] = fmt.Sprintf("must be %d to %d characters", minIDTokenLen, maxIDTokenLen)
	}
	if len(in.InviteCode) > maxInviteCodeLen {
		fields["invite_code"] = "is too long"
	}
	if len(fields) > 0 {
		return nil, apierr.Validation(fields)
	}
	// Invite codes are guessable only by trying: attempts carrying one are
	// limited per app and client address.
	if code != "" {
		if wait, ok := s.inviteLimit.Allow(in.AppID.String() + "|" + ip); !ok {
			return nil, apierr.RateLimited(wait)
		}
	}

	app, claims, err := s.lineauth.VerifyIDTokenForApp(ctx, in.AppID, in.IDToken, "", lineauth.AudienceStaff)
	if err != nil {
		return nil, err
	}

	var st Staff
	err = db.WithTenant(ctx, s.pool, app.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		sub := claims.Sub
		row, err := q.GetStaffByLineUserID(ctx, sqlc.GetStaffByLineUserIDParams{AppID: app.ID, LineUserID: &sub})
		switch {
		case err == nil:
		case db.IsNotFound(err):
			if code == "" {
				return errStaffLogin
			}
			hash := tenant.HashInviteCodeWithKey(s.inviteKey, code)
			invited, err := q.GetStaffByInviteHash(ctx, sqlc.GetStaffByInviteHashParams{
				AppID: app.ID, InviteCodeHash: &hash, Now: pgtype.Timestamptz{Time: s.clock.Now(), Valid: true},
			})
			if err != nil {
				if db.IsNotFound(err) {
					return errStaffLogin
				}
				return fmt.Errorf("staffauth: invite lookup: %w", err)
			}
			if invited.Status != "active" {
				return errStaffLogin
			}
			n, err := q.BindStaffLineUser(ctx, sqlc.BindStaffLineUserParams{ID: invited.ID, LineUserID: &sub})
			if err != nil {
				return fmt.Errorf("staffauth: bind LINE account: %w", err)
			}
			if n != 1 {
				return errStaffLogin
			}
			if row, err = q.GetStaff(ctx, invited.ID); err != nil {
				return fmt.Errorf("staffauth: reload staff: %w", err)
			}
			appID := app.ID
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: app.TenantID, AppID: &appID,
				Actor:  principal.Principal{Kind: principal.KindStaff, StaffID: row.ID, AppID: app.ID, TenantID: app.TenantID},
				Action: audit.StaffLoginBound, TargetType: "staff", TargetID: row.ID.String(),
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("staffauth: staff lookup: %w", err)
		}
		if row.Status != "active" {
			return errStaffLogin
		}
		if err := q.TouchStaffLogin(ctx, row.ID); err != nil {
			return fmt.Errorf("staffauth: touch login: %w", err)
		}
		st, err = s.withStore(ctx, q, row)
		return err
	})
	if err != nil {
		if errors.Is(err, errStaffLogin) {
			s.log.InfoContext(ctx, "staff sign-in refused", "app_id", app.ID)
		}
		return nil, err
	}
	token, err := s.signer.issue(sessionClaims{StaffID: st.ID, TenantID: st.TenantID, AppID: st.AppID}, s.sessionTTL)
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "staff signed in", "app_id", app.ID, "staff_id", st.ID)
	return &Session{Token: token, ExpiresIn: int(s.sessionTTL.Seconds()), Staff: st, App: app}, nil
}

// AuthenticateStaff verifies a staff session token and returns its principal.
// It implements httpserver.StaffAuthenticator.
func (s *Service) AuthenticateStaff(ctx context.Context, token string) (principal.Principal, error) {
	cl, err := s.signer.verify(token)
	if err != nil {
		return principal.Principal{}, errBadSession
	}
	var st sqlc.Staff
	var app sqlc.App
	err = db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		var err error
		if st, err = q.GetStaff(ctx, cl.StaffID); err != nil {
			return err
		}
		app, err = q.GetApp(ctx, st.AppID)
		return err
	})
	if err != nil {
		if db.IsNotFound(err) {
			return principal.Principal{}, errBadSession
		}
		return principal.Principal{}, fmt.Errorf("staffauth: load staff: %w", err)
	}
	switch {
	case st.TenantID != cl.TenantID || st.AppID != cl.AppID,
		st.Status != "active",
		st.LineUserID == nil || *st.LineUserID == "",
		st.SessionsValidFrom.Valid && cl.IssuedAt.Before(st.SessionsValidFrom.Time),
		app.Status != "active":
		return principal.Principal{}, errBadSession
	}
	p := principal.Principal{
		Kind:      principal.KindStaff,
		StaffID:   st.ID,
		StaffRole: st.Role,
		AppID:     st.AppID,
		TenantID:  st.TenantID,
		IssuedAt:  cl.IssuedAt,
	}
	if st.StoreID.Valid {
		p.StoreID = st.StoreID.UUID
	}
	return p, nil
}

// Me returns the signed-in staff member with their store.
func (s *Service) Me(ctx context.Context, p principal.Principal) (Staff, error) {
	if !p.IsStaff() {
		return Staff{}, errNotStaff
	}
	var st Staff
	err := db.WithTenant(ctx, s.pool, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetStaff(ctx, p.StaffID)
		if err != nil {
			if db.IsNotFound(err) {
				return errNotStaff
			}
			return fmt.Errorf("staffauth: load staff: %w", err)
		}
		st, err = s.withStore(ctx, q, row)
		return err
	})
	return st, err
}

// Scan verifies a member's rotating QR code for the staff member's app and
// returns the member, their balance and a scan token for points actions.
// Every failure is a 422 that never says whether the code belongs to another
// app.
func (s *Service) Scan(ctx context.Context, p principal.Principal, qr string) (ScanResult, error) {
	if !p.IsStaff() {
		return ScanResult{}, errNotStaff
	}
	parsed, err := card.Parse(qr)
	if err != nil || parsed.AppID != p.AppID {
		return ScanResult{}, errBadCard
	}
	var out ScanResult
	var keyVersion int
	err = db.WithTenant(ctx, s.pool, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		m, err := q.GetMember(ctx, parsed.MemberID)
		if err != nil {
			if db.IsNotFound(err) {
				return errBadCard
			}
			return fmt.Errorf("staffauth: load member: %w", err)
		}
		if m.AppID != p.AppID || m.AnonymizedAt.Valid {
			return errBadCard
		}
		// Verify checks the MAC before the window, so card_expired is only
		// ever said about a code this server issued.
		if err := s.card.Verify(parsed, int(m.QrKeyVersion)); err != nil {
			if errors.Is(err, card.ErrExpired) {
				return errCardExpired
			}
			return errBadCard
		}
		keyVersion = int(m.QrKeyVersion)
		acct, err := q.GetPointsAccount(ctx, m.ID)
		switch {
		case err == nil:
			out.Balance = acct.Balance
		case db.IsNotFound(err):
		default:
			return fmt.Errorf("staffauth: load points: %w", err)
		}
		out.Member = m
		return nil
	})
	if err != nil {
		return ScanResult{}, err
	}
	out.ScanToken, out.ExpiresAt, err = s.card.IssueScanToken(p.StaffID, out.Member.ID, p.AppID, p.TenantID, keyVersion)
	if err != nil {
		return ScanResult{}, err
	}
	s.log.DebugContext(ctx, "member card scanned", "staff_id", p.StaffID, "member_id", out.Member.ID)
	return out, nil
}

// MemberCard returns the calling member's current QR code.
func (s *Service) MemberCard(ctx context.Context, p principal.Principal) (card.Card, error) {
	if !p.IsMember() {
		return card.Card{}, apierr.Unauthorized("sign in with LINE")
	}
	var version int32
	err := db.WithTenant(ctx, s.pool, p.TenantID, func(ctx context.Context, tx pgx.Tx) (err error) {
		version, err = sqlc.New(tx).GetMemberQRVersion(ctx, p.MemberID)
		if db.IsNotFound(err) {
			return apierr.Unauthorized("sign in with LINE")
		}
		return err
	})
	if err != nil {
		return card.Card{}, err
	}
	return s.card.Card(p.AppID, p.MemberID, int(version))
}

func (s *Service) withStore(ctx context.Context, q *sqlc.Queries, row sqlc.Staff) (Staff, error) {
	st := Staff{Staff: row}
	if !row.StoreID.Valid {
		return st, nil
	}
	store, err := q.GetStore(ctx, row.StoreID.UUID)
	if err != nil {
		if db.IsNotFound(err) {
			return st, nil
		}
		return Staff{}, fmt.Errorf("staffauth: load store: %w", err)
	}
	st.StoreName = &store.Name
	return st, nil
}
