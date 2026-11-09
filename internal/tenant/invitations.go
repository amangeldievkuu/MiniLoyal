package tenant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// InviteTTL is how long an invitation link stays valid.
const InviteTTL = 7 * 24 * time.Hour

// Invitation is a pending team invitation as the dashboard shows it. The
// token is never part of it.
type Invitation struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Email     string
	Role      string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// InvitationCreated is a fresh invitation with its raw token. The token is
// shown once; only its hash is stored.
type InvitationCreated struct {
	Invitation
	Token string
}

func toInvitation(r sqlc.Invitation) Invitation {
	return Invitation{
		ID: r.ID, TenantID: r.TenantID, Email: r.Email, Role: r.Role,
		ExpiresAt: r.ExpiresAt.Time, CreatedAt: r.CreatedAt.Time,
	}
}

// CreateInvitation invites an email address to the tenant. Owners grant any
// role, admins grant admin or staff. Existing members and pending
// invitations are refused with 409. Replaces adding users by email, which
// revealed which addresses had accounts.
func (s *Service) CreateInvitation(ctx context.Context, p principal.Principal, tenantID uuid.UUID, idem Idem, email string, role principal.Role) (InvitationCreated, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		return InvitationCreated{}, apierr.Validation(map[string]string{"email": "must be a valid email address"})
	}
	if !role.Valid() {
		return InvitationCreated{}, apierr.Validation(map[string]string{"role": "must be owner, admin or staff"})
	}
	return write(ctx, s, p, tenantID, principal.RoleAdmin, idem, func(ctx context.Context, tx pgx.Tx, actor principal.Role) (InvitationCreated, error) {
		if !canGrant(actor, role) {
			return InvitationCreated{}, apierr.Forbidden("admins cannot grant the owner role")
		}
		q := sqlc.New(tx)
		now := s.clock.Now()

		// Someone who already belongs to the tenant needs no invitation. The
		// answer is the same whether or not the address has an account.
		user, err := q.GetUserByEmail(ctx, email)
		switch {
		case err == nil:
			if _, err := q.GetMembership(ctx, sqlc.GetMembershipParams{TenantID: tenantID, UserID: user.ID}); err == nil {
				return InvitationCreated{}, apierr.Conflict("that person is already a member")
			} else if !db.IsNotFound(err) {
				return InvitationCreated{}, fmt.Errorf("tenant: membership lookup: %w", err)
			}
		case !db.IsNotFound(err):
			return InvitationCreated{}, fmt.Errorf("tenant: user lookup: %w", err)
		}

		if err := q.ExpirePendingInvitation(ctx, sqlc.ExpirePendingInvitationParams{TenantID: tenantID, Email: email, Now: tstamp(now)}); err != nil {
			return InvitationCreated{}, fmt.Errorf("tenant: expire invitation: %w", err)
		}
		token := randomInviteToken()
		row, err := q.CreateInvitation(ctx, sqlc.CreateInvitationParams{
			ID:              newID(),
			TenantID:        tenantID,
			Email:           email,
			Role:            string(role),
			TokenHash:       hashInviteToken(token),
			InvitedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
			ExpiresAt:       tstamp(now.Add(InviteTTL)),
		})
		if err != nil {
			if db.IsUniqueViolation(err, "") {
				return InvitationCreated{}, apierr.Conflict("an invitation for that email is already pending")
			}
			return InvitationCreated{}, fmt.Errorf("tenant: create invitation: %w", err)
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, Actor: p, Action: audit.TeamInvite,
			TargetType: "invitation", TargetID: row.ID.String(),
			Metadata: map[string]any{"email": email, "role": string(role)},
		}); err != nil {
			return InvitationCreated{}, err
		}
		return InvitationCreated{Invitation: toInvitation(row), Token: token}, nil
	})
}

// ListInvitations returns the tenant's pending invitations.
func (s *Service) ListInvitations(ctx context.Context, p principal.Principal, tenantID uuid.UUID) ([]Invitation, error) {
	out := []Invitation{}
	err := s.read(ctx, p, tenantID, principal.RoleStaff, func(ctx context.Context, q *sqlc.Queries, _ principal.Role) error {
		rows, err := q.ListPendingInvitations(ctx, sqlc.ListPendingInvitationsParams{TenantID: tenantID, Now: tstamp(s.clock.Now())})
		if err != nil {
			return fmt.Errorf("tenant: list invitations: %w", err)
		}
		for _, r := range rows {
			out = append(out, toInvitation(r))
		}
		return nil
	})
	return out, err
}

// RevokeInvitation cancels a pending invitation. Owners and admins.
func (s *Service) RevokeInvitation(ctx context.Context, p principal.Principal, invitationID uuid.UUID) error {
	tenantID, err := s.tenantOf(ctx, "invitation", func(ctx context.Context, q *sqlc.Queries) (uuid.UUID, error) {
		return q.GetInvitationTenant(ctx, invitationID)
	})
	if err != nil {
		return err
	}
	return db.WithTenant(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.authorize(ctx, tx, p, tenantID, principal.RoleAdmin); err != nil {
			return err
		}
		q := sqlc.New(tx)
		n, err := q.RevokeInvitation(ctx, sqlc.RevokeInvitationParams{ID: invitationID, Now: tstamp(s.clock.Now())})
		if err != nil {
			return fmt.Errorf("tenant: revoke invitation: %w", err)
		}
		if n == 0 {
			return apierr.NotFound("invitation")
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: tenantID, Actor: p, Action: audit.TeamInviteRevoke,
			TargetType: "invitation", TargetID: invitationID.String(),
		})
	})
}

// randomInviteToken is 256 bits of randomness, base64url without padding.
func randomInviteToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("tenant: random: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashInviteToken is the stored form of a token: random tokens need no slow
// hash.
func hashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func tstamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
