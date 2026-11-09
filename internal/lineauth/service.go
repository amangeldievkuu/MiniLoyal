// Package lineauth signs MINI App members in with LINE: it verifies the LIFF
// ID token against the app's configured channels, creates or refreshes the
// member, and issues the one-hour member session the SDK uses.
package lineauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Defaults.
const (
	DefaultIssuer      = "miniloyal"
	DefaultSessionTTL  = time.Hour
	defaultIPPerMinute = 60
	minSigningKeyLen   = 32

	maxIDTokenLen     = 4096
	minIDTokenLen     = 20
	maxAccessTokenLen = 4096
	maxLanguageLen    = 16
)

// Config tunes the service. Zero values take the defaults.
type Config struct {
	SigningKey  []byte
	Issuer      string
	SessionTTL  time.Duration
	IPPerMinute int
	// RequireVerifiedChannels accepts an ID token only for channels whose
	// credentials were proven against LINE. Off only in development.
	RequireVerifiedChannels bool
}

// Deps are the service's collaborators. Verifier nil disables sign-in with
// 503 until the LINE verifier is wired.
type Deps struct {
	Pool     *pgxpool.Pool
	Keyring  crypto.Keyring
	Verifier IDTokenVerifier
	Clock    clock.Clock
	Log      *slog.Logger
	Config   Config
}

// Service is the member authentication service.
type Service struct {
	pool       *pgxpool.Pool
	q          *sqlc.Queries
	keyring    crypto.Keyring
	verifier   IDTokenVerifier
	clock      clock.Clock
	log        *slog.Logger
	signer     *signer
	sessionTTL time.Duration
	ipLimit    *auth.Limiter
	// requireVerified drops unverified channels from the acceptable audiences.
	requireVerified bool
}

// New builds a Service.
func New(d Deps) (*Service, error) {
	if d.Pool == nil {
		return nil, errors.New("lineauth: a database pool is required")
	}
	if len(d.Config.SigningKey) < minSigningKeyLen {
		return nil, fmt.Errorf("lineauth: signing key must be at least %d bytes", minSigningKeyLen)
	}
	if d.Keyring == nil {
		return nil, errors.New("lineauth: a keyring is required")
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
	return &Service{
		pool:            d.Pool,
		q:               sqlc.New(d.Pool),
		keyring:         d.Keyring,
		verifier:        d.Verifier,
		clock:           d.Clock,
		log:             d.Log,
		signer:          &signer{key: cfg.SigningKey, issuer: cfg.Issuer, clock: d.Clock},
		requireVerified: cfg.RequireVerifiedChannels,
		sessionTTL:      cfg.SessionTTL,
		ipLimit:         auth.NewLimiter(d.Clock, cfg.IPPerMinute, cfg.IPPerMinute),
	}, nil
}

// SessionTTL is how long a member session token lives.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// Client describes the device signing in.
type Client struct {
	IP        string
	UserAgent string
}

// LoginInput is the SDK's sign-in request.
type LoginInput struct {
	AppID           uuid.UUID
	IDToken         string
	Nonce           string
	LiffAccessToken string
	Language        *string
}

// MemberSession is a signed-in member.
type MemberSession struct {
	Token     string
	ExpiresIn int
	Member    sqlc.Member
	App       sqlc.App
	IsNew     bool
}

var (
	errAppNotFound  = apierr.NotFound("app")
	errBadIDToken   = apierr.Unauthorized("invalid LINE ID token")
	errNoChannels   = apierr.Unauthorized("the app has no LINE channel configured")
	errNoVerifier   = apierr.Unavailable("LINE sign-in is not configured")
	errBadSession   = apierr.Unauthorized("invalid or expired session")
	errMemberErased = apierr.Unauthorized("this member was erased")
)

// LoginWithLine verifies the ID token for the app and returns a session.
func (s *Service) LoginWithLine(ctx context.Context, in LoginInput, c Client) (*MemberSession, error) {
	if wait, ok := s.ipLimit.Allow("ip:" + ipKey(c.IP)); !ok {
		return nil, apierr.RateLimited(wait)
	}
	if fields := validateLogin(&in); len(fields) > 0 {
		return nil, apierr.Validation(fields)
	}
	if s.verifier == nil {
		return nil, errNoVerifier
	}

	// The app and its channels are looked up before any tenant is known.
	var app sqlc.App
	var channels []sqlc.ListAppChannelsForLoginRow
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		var err error
		app, err = q.GetApp(ctx, in.AppID)
		if err != nil {
			if db.IsNotFound(err) {
				return errAppNotFound
			}
			return fmt.Errorf("lineauth: load app: %w", err)
		}
		if app.Status != "active" {
			return errAppNotFound
		}
		channels, err = q.ListAppChannelsForLogin(ctx, app.ID)
		if err != nil {
			return fmt.Errorf("lineauth: load channels: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, errNoChannels
	}

	vin := VerifyInput{ChannelSecrets: map[string]string{}, Nonce: in.Nonce}
	for _, ch := range channels {
		if s.requireVerified && !ch.VerifiedAt.Valid {
			continue
		}
		vin.ChannelIDs = append(vin.ChannelIDs, ch.ChannelID)
		if len(ch.ChannelSecretEnc) == 0 {
			continue
		}
		secret, err := s.keyring.Decrypt(ctx, ch.ChannelSecretEnc, channelSecretAAD(app.ID, ch.Environment))
		if err != nil {
			return nil, fmt.Errorf("lineauth: decrypt channel secret: %w", err)
		}
		vin.ChannelSecrets[ch.ChannelID] = string(secret)
	}
	if len(vin.ChannelIDs) == 0 {
		return nil, errNoChannels
	}
	claims, err := s.verifier.Verify(ctx, in.IDToken, vin)
	if err != nil {
		s.log.InfoContext(ctx, "line id token rejected", "app_id", app.ID)
		return nil, errBadIDToken
	}
	if claims.Sub == "" {
		return nil, errBadIDToken
	}

	var row sqlc.UpsertMemberFromLoginRow
	err = db.WithTenant(ctx, s.pool, app.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		row, err = sqlc.New(tx).UpsertMemberFromLogin(ctx, sqlc.UpsertMemberFromLoginParams{
			ID:          uuid.Must(uuid.NewV7()),
			TenantID:    app.TenantID,
			AppID:       app.ID,
			LineUserID:  claims.Sub,
			DisplayName: nonEmpty(claims.Name),
			PictureUrl:  nonEmpty(claims.Picture),
			Language:    in.Language,
		})
		if err != nil {
			return fmt.Errorf("lineauth: upsert member: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	member := memberFromUpsert(row)
	token, err := s.signer.issue(sessionClaims{MemberID: member.ID, TenantID: member.TenantID, AppID: member.AppID}, s.sessionTTL)
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "member signed in", "app_id", app.ID, "member_id", member.ID, "new", row.Created)
	return &MemberSession{
		Token:     token,
		ExpiresIn: int(s.sessionTTL.Seconds()),
		Member:    member,
		App:       app,
		IsNew:     row.Created,
	}, nil
}

// AuthenticateMember verifies a member session token and returns its
// principal. It implements httpserver.MemberAuthenticator.
func (s *Service) AuthenticateMember(ctx context.Context, token string) (principal.Principal, error) {
	cl, err := s.signer.verify(token)
	if err != nil {
		return principal.Principal{}, errBadSession
	}
	var member sqlc.Member
	var app sqlc.App
	err = db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		var err error
		if member, err = q.GetMember(ctx, cl.MemberID); err != nil {
			return err
		}
		app, err = q.GetApp(ctx, member.AppID)
		return err
	})
	if err != nil {
		if db.IsNotFound(err) {
			return principal.Principal{}, errBadSession
		}
		return principal.Principal{}, fmt.Errorf("lineauth: load member: %w", err)
	}
	if member.AnonymizedAt.Valid {
		return principal.Principal{}, errMemberErased
	}
	// A disabled or suspended app ends its members' sessions at once.
	if app.Status != "active" {
		return principal.Principal{}, errBadSession
	}
	if member.TenantID != cl.TenantID || member.AppID != cl.AppID {
		return principal.Principal{}, errBadSession
	}
	return principal.Principal{
		Kind:     principal.KindMember,
		MemberID: member.ID,
		TenantID: member.TenantID,
		AppID:    member.AppID,
		IssuedAt: cl.IssuedAt,
	}, nil
}

// channelSecretAAD matches the convention the tenant service encrypts with.
func channelSecretAAD(appID uuid.UUID, environment string) string {
	return "app_channels.channel_secret_enc:" + appID.String() + ":" + environment
}

func validateLogin(in *LoginInput) map[string]string {
	fields := map[string]string{}
	if in.AppID == uuid.Nil {
		fields["app_id"] = "is required"
	}
	in.IDToken = strings.TrimSpace(in.IDToken)
	if n := len(in.IDToken); n < minIDTokenLen || n > maxIDTokenLen {
		fields["id_token"] = fmt.Sprintf("must be %d to %d characters", minIDTokenLen, maxIDTokenLen)
	}
	if len(in.LiffAccessToken) > maxAccessTokenLen {
		fields["liff_access_token"] = "is too long"
	}
	if len(in.Nonce) > 255 {
		fields["nonce"] = "is too long"
	}
	if in.Language != nil {
		lang := strings.TrimSpace(*in.Language)
		switch {
		case lang == "":
			in.Language = nil
		case len(lang) > maxLanguageLen:
			fields["language"] = "is too long"
		default:
			in.Language = &lang
		}
	}
	return fields
}

func ipKey(ip string) string {
	if ip == "" {
		return "unknown"
	}
	return ip
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func memberFromUpsert(r sqlc.UpsertMemberFromLoginRow) sqlc.Member {
	return sqlc.Member{
		ID: r.ID, TenantID: r.TenantID, AppID: r.AppID, LineUserID: r.LineUserID,
		DisplayName: r.DisplayName, PictureUrl: r.PictureUrl, Language: r.Language,
		IsFriend: r.IsFriend, FriendChangedAt: r.FriendChangedAt,
		Phone: r.Phone, Birthday: r.Birthday, Gender: r.Gender, CustomFields: r.CustomFields,
		ConsentVersion: r.ConsentVersion, ConsentAt: r.ConsentAt,
		LastLoginAt: r.LastLoginAt, AnonymizedAt: r.AnonymizedAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
