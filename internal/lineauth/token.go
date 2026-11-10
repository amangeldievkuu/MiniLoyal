package lineauth

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

// Member session tokens share the signing key with dashboard tokens but carry
// typ "member", so neither kind can be presented as the other.
const (
	typMember = "member"

	claimTyp      = "typ"
	claimTenantID = "tenant_id"
	claimAppID    = "app_id"
	claimIatMs    = "iat_ms"

	acceptableSkew = 30 * time.Second
)

var errInvalidToken = errors.New("lineauth: invalid session token")

type signer struct {
	key    []byte
	issuer string
	clock  clock.Clock
}

// sessionClaims identify a member session.
type sessionClaims struct {
	MemberID uuid.UUID
	TenantID uuid.UUID
	AppID    uuid.UUID
	IssuedAt time.Time
}

func (s *signer) issue(c sessionClaims, ttl time.Duration) (string, error) {
	now := s.clock.Now()
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(c.MemberID.String()).
		IssuedAt(now).
		Expiration(now.Add(ttl)).
		Claim(claimTyp, typMember).
		Claim(claimTenantID, c.TenantID.String()).
		Claim(claimAppID, c.AppID.String()).
		Claim(claimIatMs, now.UnixMilli()).
		Build()
	if err != nil {
		return "", fmt.Errorf("lineauth: build token: %w", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), s.key))
	if err != nil {
		return "", fmt.Errorf("lineauth: sign token: %w", err)
	}
	return string(signed), nil
}

// verify checks signature, issuer, expiry and kind.
func (s *signer) verify(raw string) (sessionClaims, error) {
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKey(jwa.HS256(), s.key),
		jwt.WithIssuer(s.issuer),
		jwt.WithAcceptableSkew(acceptableSkew),
		jwt.WithClock(s.clock),
	)
	if err != nil {
		return sessionClaims{}, errInvalidToken
	}
	var typ string
	if err := tok.Get(claimTyp, &typ); err != nil || typ != typMember {
		return sessionClaims{}, errInvalidToken
	}
	subject, ok := tok.Subject()
	if !ok {
		return sessionClaims{}, errInvalidToken
	}
	memberID, err := uuid.Parse(subject)
	if err != nil {
		return sessionClaims{}, errInvalidToken
	}
	tenantID, err := uuidClaim(tok, claimTenantID)
	if err != nil {
		return sessionClaims{}, errInvalidToken
	}
	appID, err := uuidClaim(tok, claimAppID)
	if err != nil {
		return sessionClaims{}, errInvalidToken
	}
	// iat_ms carries millisecond precision so app.member_sessions_valid_from
	// comparisons are exact; iat is the fallback for older tokens.
	issued, ok := issuedAtMs(tok)
	if !ok {
		if issued, ok = tok.IssuedAt(); !ok {
			return sessionClaims{}, errInvalidToken
		}
	}
	return sessionClaims{MemberID: memberID, TenantID: tenantID, AppID: appID, IssuedAt: issued}, nil
}

func uuidClaim(tok jwt.Token, name string) (uuid.UUID, error) {
	var v string
	if err := tok.Get(name, &v); err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(v)
}

// issuedAtMs reads iat_ms whatever numeric type the JSON decoder produced.
func issuedAtMs(tok jwt.Token) (time.Time, bool) {
	var v any
	if err := tok.Get(claimIatMs, &v); err != nil {
		return time.Time{}, false
	}
	switch n := v.(type) {
	case int64:
		return time.UnixMilli(n), true
	case int:
		return time.UnixMilli(int64(n)), true
	case float64:
		return time.UnixMilli(int64(n)), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return time.Time{}, false
		}
		return time.UnixMilli(i), true
	}
	return time.Time{}, false
}
