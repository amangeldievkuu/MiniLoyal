package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

var errSessionExpired = apierr.Unauthorized("session expired")

// Refresh exchanges a refresh token for a new session, rotating the token.
//
// A token that was already rotated is normally a replay: the whole family is
// revoked. Unless its replacements are all still unused, which is what a lost
// response (or two tabs refreshing together) looks like; then one more
// replacement is issued, up to MaxChildren.
func (s *Service) Refresh(ctx context.Context, rawRefresh string, c Client) (*Session, error) {
	if err := s.allowIP(c.IP); err != nil {
		return nil, err
	}
	if rawRefresh == "" {
		return nil, apierr.Unauthorized("not signed in")
	}
	stored, err := s.q.GetRefreshTokenByHash(ctx, tokenHash(rawRefresh))
	if err != nil {
		if db.IsNotFound(err) {
			return nil, errSessionExpired
		}
		return nil, fmt.Errorf("auth: load refresh token: %w", err)
	}
	if !stored.ExpiresAt.Valid || !stored.ExpiresAt.Time.After(s.clock.Now()) {
		return nil, errSessionExpired
	}
	user, err := s.q.GetUser(ctx, stored.UserID)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, errSessionExpired
		}
		return nil, fmt.Errorf("auth: load user: %w", err)
	}
	s.sweepAsync(stored.UserID)

	parent := uuid.NullUUID{UUID: stored.ID, Valid: true}
	if !stored.RevokedAt.Valid {
		// Revoke and replace in one transaction: a parallel refresh with the
		// same cookie waits on the row, then finds it revoked and its
		// successor already stored.
		var sess *Session
		err := s.inTx(ctx, func(q *sqlc.Queries) error {
			n, err := q.RevokeRefreshToken(ctx, stored.ID)
			if err != nil {
				return fmt.Errorf("auth: revoke refresh token: %w", err)
			}
			if n != 1 {
				return nil
			}
			sess, err = s.issueSession(ctx, q, user, stored.Family, parent, c)
			return err
		})
		if err != nil {
			return nil, err
		}
		if sess != nil {
			return sess, nil
		}
	}

	// Already rotated: a replacement the client never got (or another tab),
	// or a replayed stolen token.
	unused, err := s.replacementUnused(ctx, stored.ID)
	if err != nil {
		return nil, err
	}
	if unused {
		var sess *Session
		err := s.inTx(ctx, func(q *sqlc.Queries) error {
			var err error
			sess, err = s.issueSession(ctx, q, user, stored.Family, parent, c)
			return err
		})
		if err != nil {
			return nil, err
		}
		s.log.InfoContext(ctx, "refresh token reused after a lost response", "user_id", user.ID)
		return sess, nil
	}
	if err := s.q.RevokeRefreshTokenFamily(ctx, stored.Family); err != nil {
		return nil, fmt.Errorf("auth: revoke family: %w", err)
	}
	s.log.WarnContext(ctx, "refresh token replay: family revoked", "user_id", user.ID)
	return nil, errSessionExpired
}

// replacementUnused reports whether a rotated token's replacements are all
// still unused, so the client plainly never received one. A replacement that
// was itself rotated or revoked means the client did move on and this is a
// replay; so does a token replaced too many times.
func (s *Service) replacementUnused(ctx context.Context, id uuid.UUID) (bool, error) {
	children, err := s.q.ListRefreshTokenChildren(ctx, uuid.NullUUID{UUID: id, Valid: true})
	if err != nil {
		return false, fmt.Errorf("auth: list replacements: %w", err)
	}
	if len(children) == 0 || len(children) >= MaxChildren {
		return false, nil
	}
	for _, child := range children {
		if child.RevokedAt.Valid {
			return false, nil
		}
	}
	return true, nil
}

// sweepAsync drops the account's spent tokens; nothing waits on it.
func (s *Service) sweepAsync(userID uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.q.DeleteSpentRefreshTokensForUser(ctx, userID); err != nil {
			s.log.Debug("refresh token sweep failed", "user_id", userID, "err", err)
		}
	}()
}

// SweepAll deletes every expired or long-revoked refresh token. A worker job
// runs it nightly for accounts that never come back to refresh.
func (s *Service) SweepAll(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteSpentRefreshTokens(ctx)
	if err != nil {
		return 0, fmt.Errorf("auth: sweep refresh tokens: %w", err)
	}
	return n, nil
}
