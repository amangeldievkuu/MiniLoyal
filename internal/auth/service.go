// Package auth signs dashboard users in and keeps them signed in: argon2id
// passwords, short-lived access tokens, rotating refresh tokens, TOTP with
// recovery codes, and rate limits on the credential endpoints.
//
// Reusing an already-rotated refresh token normally revokes the whole family,
// which signs out a thief and the victim alike rather than leaving a stolen
// session. The exception: a rotated token whose replacements are all still
// unused may be used again a bounded number of times. That is what a lost
// response looks like (a flaky connection, a client timeout, two tabs
// refreshing together), and treating it as theft would sign people out for
// no reason.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Encryptor protects TOTP secrets at rest. The platform keyring satisfies it.
type Encryptor interface {
	Encrypt(ctx context.Context, plaintext []byte, aad string) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte, aad string) ([]byte, error)
}

// Defaults.
const (
	DefaultIssuer     = "miniloyal"
	DefaultAccessTTL  = 15 * time.Minute
	DefaultMFATTL     = 5 * time.Minute
	DefaultRefreshTTL = 30 * 24 * time.Hour
	// MaxChildren is how many replacements one refresh token may get: its
	// rotation, then this many lost responses.
	MaxChildren = 3

	defaultIPPerMinute    = 10
	defaultEmailPerMinute = 5
	minSigningKeyLen      = 32
	tenantPlanFree        = "free"
)

// Config tunes the service. Zero values take the defaults above.
type Config struct {
	SigningKey     []byte
	Issuer         string
	AccessTTL      time.Duration
	MFATTL         time.Duration
	RefreshTTL     time.Duration
	IPPerMinute    int
	EmailPerMinute int
}

// Deps are the service's collaborators.
type Deps struct {
	Pool      *pgxpool.Pool
	Clock     clock.Clock
	Log       *slog.Logger
	Encryptor Encryptor // nil disables TOTP enrollment
	Config    Config
}

// Service is the dashboard authentication service.
type Service struct {
	pool       *pgxpool.Pool
	q          *sqlc.Queries
	clock      clock.Clock
	log        *slog.Logger
	enc        Encryptor
	signer     *signer
	accessTTL  time.Duration
	mfaTTL     time.Duration
	refreshTTL time.Duration
	dummyHash  string
	ipLimit    *Limiter
	emailLimit *Limiter
}

// New builds a Service. The dummy hash is derived once so sign-in attempts
// for unknown emails cost the same as for known ones.
func New(d Deps) (*Service, error) {
	if d.Pool == nil {
		return nil, errors.New("auth: a database pool is required")
	}
	if len(d.Config.SigningKey) < minSigningKeyLen {
		return nil, fmt.Errorf("auth: signing key must be at least %d bytes", minSigningKeyLen)
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
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = DefaultAccessTTL
	}
	if cfg.MFATTL == 0 {
		cfg.MFATTL = DefaultMFATTL
	}
	if cfg.RefreshTTL == 0 {
		cfg.RefreshTTL = DefaultRefreshTTL
	}
	if cfg.IPPerMinute == 0 {
		cfg.IPPerMinute = defaultIPPerMinute
	}
	if cfg.EmailPerMinute == 0 {
		cfg.EmailPerMinute = defaultEmailPerMinute
	}
	dummy, err := HashPassword(randomToken(16))
	if err != nil {
		return nil, err
	}
	return &Service{
		pool:       d.Pool,
		q:          sqlc.New(d.Pool),
		clock:      d.Clock,
		log:        d.Log,
		enc:        d.Encryptor,
		signer:     &signer{key: cfg.SigningKey, issuer: cfg.Issuer, clock: d.Clock},
		accessTTL:  cfg.AccessTTL,
		mfaTTL:     cfg.MFATTL,
		refreshTTL: cfg.RefreshTTL,
		dummyHash:  dummy,
		ipLimit:    NewLimiter(d.Clock, cfg.IPPerMinute, cfg.IPPerMinute),
		emailLimit: NewLimiter(d.Clock, cfg.EmailPerMinute, cfg.EmailPerMinute),
	}, nil
}

// RefreshTTL is how long a refresh token (and its cookie) lives.
func (s *Service) RefreshTTL() time.Duration { return s.refreshTTL }

// Client describes the device signing in, for session records and limits.
type Client struct {
	IP        string
	UserAgent string
}

// User is the dashboard user as shown to the client.
type User struct {
	ID          uuid.UUID
	Email       string
	DisplayName *string
	TotpEnabled bool
	CreatedAt   time.Time
}

// Tenant is a tenant the user belongs to.
type Tenant struct {
	ID             uuid.UUID
	Type           string
	Name           string
	Slug           string
	Plan           string
	ParentTenantID *uuid.UUID
	CreatedAt      time.Time
}

// Membership is a tenant plus the user's role in it.
type Membership struct {
	Tenant Tenant
	Role   string
}

// Session is a signed-in device. RefreshToken is the raw token; the HTTP
// layer stores it in the cookie and never shows it in a body.
type Session struct {
	AccessToken  string
	ExpiresIn    int
	RefreshToken string
	User         User
}

// MFAChallenge asks the client for a second factor.
type MFAChallenge struct {
	Token     string
	ExpiresIn int
}

// LoginResult holds exactly one of Session or MFA.
type LoginResult struct {
	Session *Session
	MFA     *MFAChallenge
}

// RegisterInput creates an account and its first tenant.
type RegisterInput struct {
	Email       string
	Password    string
	DisplayName *string
	TenantName  string
	TenantType  string
}

var errUnauthorized = apierr.Unauthorized("wrong email or password")

// Register creates the user, a tenant on the free plan and the owner
// membership, then signs the user in.
func (s *Service) Register(ctx context.Context, in RegisterInput, c Client) (*Session, error) {
	if err := s.allowIP(c.IP); err != nil {
		return nil, err
	}
	email, fields := validateRegister(in)
	if len(fields) > 0 {
		return nil, apierr.Validation(fields)
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	var sess *Session
	err = db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		user, err := q.CreateUser(ctx, sqlc.CreateUserParams{
			ID: newID(), Email: email, PasswordHash: hash, DisplayName: in.DisplayName,
		})
		if err != nil {
			if db.IsUniqueViolation(err, "") {
				return apierr.Conflict("email already registered")
			}
			return fmt.Errorf("auth: create user: %w", err)
		}
		slug, err := s.freeSlug(ctx, q, in.TenantName)
		if err != nil {
			return err
		}
		tenant, err := q.CreateTenant(ctx, sqlc.CreateTenantParams{
			ID: newID(), Type: in.TenantType, Name: strings.TrimSpace(in.TenantName), Slug: slug, Plan: tenantPlanFree,
		})
		if err != nil {
			if db.IsUniqueViolation(err, "") {
				return apierr.Conflict("tenant name is taken, try again")
			}
			return fmt.Errorf("auth: create tenant: %w", err)
		}
		if _, err := q.InsertMembership(ctx, sqlc.InsertMembershipParams{
			TenantID: tenant.ID, UserID: user.ID, Role: string(principal.RoleOwner),
		}); err != nil {
			return fmt.Errorf("auth: owner membership: %w", err)
		}
		sess, err = s.issueSession(ctx, q, user, uuid.New(), uuid.NullUUID{}, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "dashboard user registered", "user_id", sess.User.ID)
	return sess, nil
}

// Login checks the password. It verifies against a dummy hash when the email
// is unknown so response time does not reveal which accounts exist.
func (s *Service) Login(ctx context.Context, email, password string, c Client) (*LoginResult, error) {
	if err := s.allowIP(c.IP); err != nil {
		return nil, err
	}
	email = normalizeEmail(email)
	if wait, ok := s.emailLimit.Allow("email:" + email); !ok {
		return nil, apierr.RateLimited(wait)
	}

	user, err := s.q.GetUserByEmail(ctx, email)
	switch {
	case db.IsNotFound(err):
		_, _ = VerifyPassword(s.dummyHash, password)
		return nil, errUnauthorized
	case err != nil:
		return nil, fmt.Errorf("auth: load user: %w", err)
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil {
		s.log.ErrorContext(ctx, "stored password hash is unreadable", "user_id", user.ID, "err", err)
		return nil, errUnauthorized
	}
	if !ok {
		s.log.InfoContext(ctx, "dashboard login rejected", "user_id", user.ID)
		return nil, errUnauthorized
	}

	if user.TotpEnabledAt.Valid {
		token, _, err := s.signer.issue(user.ID, typMFA, s.mfaTTL)
		if err != nil {
			return nil, err
		}
		return &LoginResult{MFA: &MFAChallenge{Token: token, ExpiresIn: int(s.mfaTTL.Seconds())}}, nil
	}
	sess, err := s.signInUser(ctx, user, c)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Session: sess}, nil
}

// CompleteMFA finishes a sign-in that returned an MFA challenge. code is a
// six-digit TOTP code or a recovery code.
func (s *Service) CompleteMFA(ctx context.Context, mfaToken, code string, c Client) (*Session, error) {
	if err := s.allowIP(c.IP); err != nil {
		return nil, err
	}
	cl, err := s.signer.verify(mfaToken, typMFA)
	if err != nil {
		return nil, apierr.Unauthorized("invalid or expired challenge")
	}
	user, err := s.q.GetUser(ctx, cl.Subject)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, apierr.Unauthorized("invalid or expired challenge")
		}
		return nil, fmt.Errorf("auth: load user: %w", err)
	}
	if !user.TotpEnabledAt.Valid {
		return nil, apierr.Unauthorized("invalid or expired challenge")
	}
	ok, err := s.checkSecondFactor(ctx, user, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		s.log.InfoContext(ctx, "mfa code rejected", "user_id", user.ID)
		return nil, apierr.Unauthorized("invalid code")
	}
	return s.signInUser(ctx, user, c)
}

// signInUser records the login and issues a session in a new token family.
func (s *Service) signInUser(ctx context.Context, user sqlc.User, c Client) (*Session, error) {
	var sess *Session
	err := s.inTx(ctx, func(q *sqlc.Queries) error {
		if err := q.TouchUserLogin(ctx, user.ID); err != nil {
			return fmt.Errorf("auth: touch login: %w", err)
		}
		var err error
		sess, err = s.issueSession(ctx, q, user, uuid.New(), uuid.NullUUID{}, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "dashboard login", "user_id", user.ID)
	return sess, nil
}

// Logout revokes the refresh token in the cookie. Unknown tokens are fine.
func (s *Service) Logout(ctx context.Context, rawRefresh string) error {
	if rawRefresh == "" {
		return nil
	}
	if _, err := s.q.RevokeRefreshTokenByHash(ctx, tokenHash(rawRefresh)); err != nil {
		return fmt.Errorf("auth: revoke refresh token: %w", err)
	}
	return nil
}

// LogoutAll ends every session of the account on every device, including
// access tokens already issued.
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	now := s.clock.Now()
	err := s.inTx(ctx, func(q *sqlc.Queries) error {
		if err := q.InvalidateUserSessionsAt(ctx, sqlc.InvalidateUserSessionsAtParams{ID: userID, SessionsValidFrom: ts(now)}); err != nil {
			return fmt.Errorf("auth: invalidate sessions: %w", err)
		}
		if err := q.RevokeUserRefreshTokens(ctx, userID); err != nil {
			return fmt.Errorf("auth: revoke refresh tokens: %w", err)
		}
		return nil
	})
	if err == nil {
		s.log.InfoContext(ctx, "signed out everywhere", "user_id", userID)
	}
	return err
}

// ChangePassword replaces the password and ends every other session; this
// device gets a new one. A wrong current password is 403, not 401, which
// clients take to mean the session itself is gone.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, next string, c Client) (*Session, error) {
	user, err := s.q.GetUser(ctx, userID)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, apierr.Unauthorized("")
		}
		return nil, fmt.Errorf("auth: load user: %w", err)
	}
	ok, err := VerifyPassword(user.PasswordHash, current)
	if err != nil || !ok {
		return nil, apierr.Forbidden("current password is wrong")
	}
	if msg := validPassword(next); msg != "" {
		return nil, apierr.Validation(map[string]string{"new_password": msg})
	}
	hash, err := HashPassword(next)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	var sess *Session
	err = s.inTx(ctx, func(q *sqlc.Queries) error {
		if err := q.UpdateUserPasswordAt(ctx, sqlc.UpdateUserPasswordAtParams{ID: userID, PasswordHash: hash, SessionsValidFrom: ts(now)}); err != nil {
			return fmt.Errorf("auth: update password: %w", err)
		}
		if err := q.RevokeUserRefreshTokens(ctx, userID); err != nil {
			return fmt.Errorf("auth: revoke refresh tokens: %w", err)
		}
		user.PasswordHash = hash
		var err error
		sess, err = s.issueSession(ctx, q, user, uuid.New(), uuid.NullUUID{}, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "password changed", "user_id", userID)
	return sess, nil
}

// Me returns the user and their tenant memberships.
func (s *Service) Me(ctx context.Context, userID uuid.UUID) (*User, []Membership, error) {
	user, err := s.q.GetUser(ctx, userID)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, nil, apierr.Unauthorized("")
		}
		return nil, nil, fmt.Errorf("auth: load user: %w", err)
	}
	var rows []sqlc.ListMembershipsForUserRow
	err = db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = sqlc.New(tx).ListMembershipsForUser(ctx, userID)
		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("auth: list memberships: %w", err)
	}
	memberships := make([]Membership, 0, len(rows))
	for _, r := range rows {
		memberships = append(memberships, Membership{Tenant: toTenant(r.Tenant), Role: r.Role})
	}
	u := toUser(user)
	return &u, memberships, nil
}

// AuthenticateDashboard verifies an access token and returns its principal.
// It implements httpserver.DashboardAuthenticator.
func (s *Service) AuthenticateDashboard(ctx context.Context, token string) (principal.Principal, error) {
	cl, err := s.signer.verify(token, typDashboard)
	if err != nil {
		return principal.Principal{}, apierr.Unauthorized("invalid or expired session")
	}
	user, err := s.q.GetUser(ctx, cl.Subject)
	if err != nil {
		if db.IsNotFound(err) {
			return principal.Principal{}, apierr.Unauthorized("invalid or expired session")
		}
		return principal.Principal{}, fmt.Errorf("auth: load user: %w", err)
	}
	if user.SessionsValidFrom.Valid && cl.IssuedAt.Before(user.SessionsValidFrom.Time) {
		return principal.Principal{}, apierr.Unauthorized("session expired")
	}
	return principal.Principal{Kind: principal.KindUser, UserID: user.ID, IssuedAt: cl.IssuedAt}, nil
}

// issueSession stores a refresh token in family (replacing parent, if any)
// and signs an access token.
func (s *Service) issueSession(ctx context.Context, q *sqlc.Queries, user sqlc.User, family uuid.UUID, parent uuid.NullUUID, c Client) (*Session, error) {
	raw := randomToken(32)
	var ua *string
	if c.UserAgent != "" {
		v := c.UserAgent
		ua = &v
	}
	if _, err := q.CreateRefreshToken(ctx, sqlc.CreateRefreshTokenParams{
		ID:        newID(),
		UserID:    user.ID,
		TokenHash: tokenHash(raw),
		Family:    family,
		ParentID:  parent,
		ExpiresAt: ts(s.clock.Now().Add(s.refreshTTL)),
		UserAgent: ua,
	}); err != nil {
		return nil, fmt.Errorf("auth: store refresh token: %w", err)
	}
	access, _, err := s.signer.issue(user.ID, typDashboard, s.accessTTL)
	if err != nil {
		return nil, err
	}
	return &Session{
		AccessToken:  access,
		ExpiresIn:    int(s.accessTTL.Seconds()),
		RefreshToken: raw,
		User:         toUser(user),
	}, nil
}

// inTx runs fn in a plain transaction. The identity tables (users,
// refresh_tokens, totp_recovery_codes) carry no tenant policy.
func (s *Service) inTx(ctx context.Context, fn func(q *sqlc.Queries) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(sqlc.New(tx)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit: %w", err)
	}
	return nil
}

func (s *Service) allowIP(ip string) error {
	if ip == "" {
		ip = "unknown"
	}
	if wait, ok := s.ipLimit.Allow("ip:" + ip); !ok {
		return apierr.RateLimited(wait)
	}
	return nil
}

var slugClean = regexp.MustCompile(`[^a-z0-9]+`)

// freeSlug derives a URL-safe slug from the tenant name and avoids collisions.
func (s *Service) freeSlug(ctx context.Context, q *sqlc.Queries, name string) (string, error) {
	base := strings.Trim(slugClean.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if len(base) < 2 {
		base = "t-" + randomHex(3)
	}
	candidate := base
	for i := 0; i < 5; i++ {
		exists, err := q.TenantSlugExists(ctx, candidate)
		if err != nil {
			return "", fmt.Errorf("auth: slug lookup: %w", err)
		}
		if !exists {
			return candidate, nil
		}
		candidate = base + "-" + randomHex(2)
	}
	return base + "-" + randomHex(4), nil
}

func validateRegister(in RegisterInput) (string, map[string]string) {
	fields := map[string]string{}
	email := normalizeEmail(in.Email)
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		fields["email"] = "must be a valid email address"
	}
	if msg := validPassword(in.Password); msg != "" {
		fields["password"] = msg
	}
	if n := len(strings.TrimSpace(in.TenantName)); n < 1 || n > 100 {
		fields["tenant_name"] = "must be 1 to 100 characters"
	}
	if in.TenantType != "brand" && in.TenantType != "agency" {
		fields["tenant_type"] = "must be brand or agency"
	}
	if in.DisplayName != nil && len(*in.DisplayName) > 100 {
		fields["display_name"] = "must be at most 100 characters"
	}
	return email, fields
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func toUser(u sqlc.User) User {
	return User{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, TotpEnabled: u.TotpEnabledAt.Valid, CreatedAt: u.CreatedAt.Time}
}

func toTenant(t sqlc.Tenant) Tenant {
	out := Tenant{ID: t.ID, Type: t.Type, Name: t.Name, Slug: t.Slug, Plan: t.Plan, CreatedAt: t.CreatedAt.Time}
	if t.ParentTenantID.Valid {
		id := t.ParentTenantID.UUID
		out.ParentTenantID = &id
	}
	return out
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// randomToken returns n random bytes, base64url encoded without padding.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("auth: random: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("auth: random: %v", err))
	}
	return hex.EncodeToString(b)
}

// tokenHash is the stored form of a refresh token: random tokens need no
// slow hash.
func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
