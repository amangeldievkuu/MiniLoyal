package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// Token kinds carried in the private "typ" claim, so an MFA challenge can
// never be used as a session and the other way round.
const (
	typDashboard = "dashboard"
	typMFA       = "mfa"

	claimTyp   = "typ"
	claimIatMs = "iat_ms"

	// acceptableSkew tolerates small clock differences between servers.
	acceptableSkew = 30 * time.Second
)

var errInvalidToken = errors.New("auth: invalid token")

// signer issues and verifies HS256 JWTs.
type signer struct {
	key    []byte
	issuer string
	clock  clock.Clock
}

// claims is what verify extracts.
type claims struct {
	Subject  uuid.UUID
	IssuedAt time.Time
	Typ      string
}

// issue signs a token for sub of the given kind. IssuedAt carries millisecond
// precision in iat_ms so sessions_valid_from comparisons are exact.
func (s *signer) issue(sub uuid.UUID, typ string, ttl time.Duration) (string, time.Time, error) {
	now := s.clock.Now()
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(sub.String()).
		IssuedAt(now).
		Expiration(now.Add(ttl)).
		Claim(claimTyp, typ).
		Claim(claimIatMs, now.UnixMilli()).
		Build()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: build token: %w", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), s.key))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign token: %w", err)
	}
	return string(signed), now, nil
}

// verify checks signature, issuer, expiry and kind, and returns the claims.
func (s *signer) verify(raw, typ string) (claims, error) {
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKey(jwa.HS256(), s.key),
		jwt.WithIssuer(s.issuer),
		jwt.WithAcceptableSkew(acceptableSkew),
		jwt.WithClock(s.clock),
	)
	if err != nil {
		return claims{}, errInvalidToken
	}
	var gotTyp string
	if err := tok.Get(claimTyp, &gotTyp); err != nil || gotTyp != typ {
		return claims{}, errInvalidToken
	}
	subject, ok := tok.Subject()
	if !ok {
		return claims{}, errInvalidToken
	}
	id, err := uuid.Parse(subject)
	if err != nil {
		return claims{}, errInvalidToken
	}
	issued, ok := issuedAtMs(tok)
	if !ok {
		if issued, ok = tok.IssuedAt(); !ok {
			return claims{}, errInvalidToken
		}
	}
	return claims{Subject: id, IssuedAt: issued, Typ: gotTyp}, nil
}

// issuedAtMs reads iat_ms whatever numeric type the JSON decoder produced.
func issuedAtMs(tok jwt.Token) (time.Time, bool) {
	var v any
	if err := tok.Get(claimIatMs, &v); err != nil {
		return time.Time{}, false
	}
	var ms int64
	switch n := v.(type) {
	case int64:
		ms = n
	case int:
		ms = int64(n)
	case float64:
		ms = int64(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return time.Time{}, false
		}
		ms = i
	default:
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}
