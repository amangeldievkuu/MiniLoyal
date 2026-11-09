package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

var errBadInvitation = apierr.Unauthorized("invalid, expired or already used invitation")

// AcceptInvitation redeems an invitation link. Opening the link proves the
// invitee owns the address. A new address gets an account (the password is
// required); an existing account confirms its password. Either way the
// membership is created and the user is signed in.
func (s *Service) AcceptInvitation(ctx context.Context, token string, password, displayName *string, c Client) (*Session, error) {
	if err := s.allowIP(c.IP); err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errBadInvitation
	}
	if displayName != nil && len(*displayName) > 100 {
		return nil, apierr.Validation(map[string]string{"display_name": "must be at most 100 characters"})
	}

	var sess *Session
	err := db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		now := s.clock.Now()
		inv, err := q.GetInvitationByTokenHash(ctx, sqlc.GetInvitationByTokenHashParams{TokenHash: tokenHash(token), Now: ts(now)})
		if err != nil {
			if db.IsNotFound(err) {
				return errBadInvitation
			}
			return fmt.Errorf("auth: load invitation: %w", err)
		}

		user, err := q.GetUserByEmail(ctx, inv.Email)
		switch {
		case db.IsNotFound(err):
			if password == nil || *password == "" {
				return apierr.Validation(map[string]string{"password": "is required to create your account"})
			}
			if msg := validPassword(*password); msg != "" {
				return apierr.Validation(map[string]string{"password": msg})
			}
			hash, err := HashPassword(*password)
			if err != nil {
				return err
			}
			user, err = q.CreateUser(ctx, sqlc.CreateUserParams{ID: newID(), Email: inv.Email, PasswordHash: hash, DisplayName: displayName})
			if err != nil {
				return fmt.Errorf("auth: create invited user: %w", err)
			}
		case err != nil:
			return fmt.Errorf("auth: load user: %w", err)
		default:
			if password == nil || *password == "" {
				return apierr.Validation(map[string]string{"password": "is required to confirm your account"})
			}
			ok, err := VerifyPassword(user.PasswordHash, *password)
			if err != nil || !ok {
				s.noteEvent(ctx, user.ID, EventLoginFailed, c, map[string]any{"via": "invitation"})
				return apierr.Unauthorized("wrong password")
			}
		}

		// A unique violation would abort the transaction, so check first.
		if _, err := q.GetMembership(ctx, sqlc.GetMembershipParams{TenantID: inv.TenantID, UserID: user.ID}); err != nil {
			if !db.IsNotFound(err) {
				return fmt.Errorf("auth: membership lookup: %w", err)
			}
			if _, err := q.InsertMembership(ctx, sqlc.InsertMembershipParams{TenantID: inv.TenantID, UserID: user.ID, Role: inv.Role}); err != nil {
				return fmt.Errorf("auth: create membership: %w", err)
			}
		}
		n, err := q.AcceptInvitation(ctx, sqlc.AcceptInvitationParams{ID: inv.ID, AcceptedByUserID: uuid.NullUUID{UUID: user.ID, Valid: true}, Now: ts(now)})
		if err != nil {
			return fmt.Errorf("auth: accept invitation: %w", err)
		}
		if n != 1 {
			return errBadInvitation
		}
		if err := s.recordEvent(ctx, q, user.ID, EventInviteAccepted, c, map[string]any{"tenant_id": inv.TenantID.String(), "role": inv.Role}); err != nil {
			return err
		}
		if err := q.TouchUserLogin(ctx, user.ID); err != nil {
			return fmt.Errorf("auth: touch login: %w", err)
		}
		sess, err = s.issueSession(ctx, q, user, uuid.New(), uuid.NullUUID{}, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "invitation accepted", "user_id", sess.User.ID)
	return sess, nil
}
