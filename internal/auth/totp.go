package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

const totpIssuer = "MiniLoyal"

var totpOpts = totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

var errMFAUnavailable = apierr.Unavailable("multi-factor authentication is not configured")

func totpAAD(userID uuid.UUID) string { return "users.totp_secret_enc:" + userID.String() }

// EnrollTOTP starts enrollment: a new secret is stored (encrypted) as
// pending and returned with its otpauth URL. Nothing is enforced until
// ConfirmTOTP succeeds. Calling again replaces the pending secret.
func (s *Service) EnrollTOTP(ctx context.Context, userID uuid.UUID) (secret, otpauthURL string, err error) {
	if s.enc == nil {
		return "", "", errMFAUnavailable
	}
	user, err := s.loadUser(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if user.TotpEnabledAt.Valid {
		return "", "", apierr.Conflict("TOTP is already enabled")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpIssuer, AccountName: user.Email, Period: totpOpts.Period, Digits: totpOpts.Digits, Algorithm: totpOpts.Algorithm})
	if err != nil {
		return "", "", fmt.Errorf("auth: generate totp: %w", err)
	}
	ct, err := s.enc.Encrypt(ctx, []byte(key.Secret()), totpAAD(userID))
	if err != nil {
		return "", "", fmt.Errorf("auth: encrypt totp secret: %w", err)
	}
	n, err := s.q.SetPendingTotp(ctx, sqlc.SetPendingTotpParams{ID: userID, TotpSecretEnc: ct})
	if err != nil {
		return "", "", fmt.Errorf("auth: store pending totp: %w", err)
	}
	if n != 1 {
		return "", "", apierr.Conflict("TOTP is already enabled")
	}
	return key.Secret(), key.URL(), nil
}

// ConfirmTOTP enables TOTP once the user proves they hold the secret, and
// returns fresh recovery codes (shown once).
func (s *Service) ConfirmTOTP(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	if s.enc == nil {
		return nil, errMFAUnavailable
	}
	user, err := s.loadUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.TotpEnabledAt.Valid {
		return nil, apierr.Conflict("TOTP is already enabled")
	}
	if len(user.TotpSecretEnc) == 0 {
		return nil, apierr.Conflict("no pending TOTP enrollment")
	}
	secret, err := s.enc.Decrypt(ctx, user.TotpSecretEnc, totpAAD(userID))
	if err != nil {
		return nil, fmt.Errorf("auth: decrypt totp secret: %w", err)
	}
	ok, err := s.validateTOTP(ctx, userID, string(secret), code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.BadRequest("invalid code")
	}

	codes := make([]string, 0, recoveryCodes)
	for i := 0; i < recoveryCodes; i++ {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	err = s.inTx(ctx, func(q *sqlc.Queries) error {
		n, err := q.EnableTotp(ctx, userID)
		if err != nil {
			return fmt.Errorf("auth: enable totp: %w", err)
		}
		if n != 1 {
			return apierr.Conflict("TOTP is already enabled")
		}
		return s.replaceRecoveryCodes(ctx, q, userID, codes)
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "totp enabled", "user_id", userID)
	return codes, nil
}

// DisableTOTP turns TOTP off after the password and a valid code (TOTP or
// recovery) are presented.
func (s *Service) DisableTOTP(ctx context.Context, userID uuid.UUID, password, code string) error {
	if wait, ok := s.userLimit.Allow("pw:" + userID.String()); !ok {
		return apierr.RateLimited(wait)
	}
	user, err := s.loadUser(ctx, userID)
	if err != nil {
		return err
	}
	if !user.TotpEnabledAt.Valid {
		return apierr.BadRequest("TOTP is not enabled")
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		return apierr.Forbidden("password is wrong")
	}
	ok, err = s.checkSecondFactor(ctx, user, code)
	if err != nil {
		return err
	}
	if !ok {
		return apierr.Forbidden("code is wrong")
	}
	err = s.inTx(ctx, func(q *sqlc.Queries) error {
		if err := q.DisableTotp(ctx, userID); err != nil {
			return fmt.Errorf("auth: disable totp: %w", err)
		}
		if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
			return fmt.Errorf("auth: delete recovery codes: %w", err)
		}
		return nil
	})
	if err == nil {
		s.log.InfoContext(ctx, "totp disabled", "user_id", userID)
	}
	return err
}

// checkSecondFactor accepts a TOTP code or an unused recovery code.
func (s *Service) checkSecondFactor(ctx context.Context, user sqlc.User, code string) (bool, error) {
	if looksLikeTOTP(code) {
		if s.enc == nil || len(user.TotpSecretEnc) == 0 {
			return false, nil
		}
		secret, err := s.enc.Decrypt(ctx, user.TotpSecretEnc, totpAAD(user.ID))
		if err != nil {
			return false, fmt.Errorf("auth: decrypt totp secret: %w", err)
		}
		return s.validateTOTP(ctx, user.ID, string(secret), code)
	}
	n, err := s.q.UseRecoveryCode(ctx, sqlc.UseRecoveryCodeParams{UserID: user.ID, CodeHash: hashRecoveryCode(code)})
	if err != nil {
		return false, fmt.Errorf("auth: use recovery code: %w", err)
	}
	return n == 1, nil
}

// validateTOTP accepts code for the current 30-second step or one step either
// side, and only when that step is newer than the last accepted one, so a code
// observed in transit cannot be replayed inside its validity window. The
// step advance is a conditional UPDATE, so concurrent attempts cannot both win.
func (s *Service) validateTOTP(ctx context.Context, userID uuid.UUID, secret, code string) (bool, error) {
	period := int64(totpOpts.Period)
	base := s.clock.Now().Unix() / period
	matched := int64(-1)
	for _, d := range []int64{0, -1, 1} {
		step := base + d
		want, err := totp.GenerateCodeCustom(secret, time.Unix(step*period, 0), totpOpts)
		if err != nil {
			return false, fmt.Errorf("auth: totp: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			matched = step
			break
		}
	}
	if matched < 0 {
		return false, nil
	}
	n, err := s.q.SetTotpLastStep(ctx, sqlc.SetTotpLastStepParams{ID: userID, TotpLastStep: &matched})
	if err != nil {
		return false, fmt.Errorf("auth: record totp step: %w", err)
	}
	return n == 1, nil
}

func (s *Service) replaceRecoveryCodes(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, codes []string) error {
	if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return fmt.Errorf("auth: delete recovery codes: %w", err)
	}
	for _, c := range codes {
		if err := q.InsertRecoveryCode(ctx, sqlc.InsertRecoveryCodeParams{UserID: userID, CodeHash: hashRecoveryCode(c)}); err != nil {
			return fmt.Errorf("auth: insert recovery code: %w", err)
		}
	}
	return nil
}

func (s *Service) loadUser(ctx context.Context, userID uuid.UUID) (sqlc.User, error) {
	user, err := s.q.GetUser(ctx, userID)
	if err != nil {
		if db.IsNotFound(err) {
			return sqlc.User{}, apierr.Unauthorized("")
		}
		return sqlc.User{}, fmt.Errorf("auth: load user: %w", err)
	}
	return user, nil
}
