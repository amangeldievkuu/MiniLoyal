package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// rewrite re-encodes a compact JWT after fn edited its header and payload,
// keeping the original signature unless sig is non-nil.
func rewrite(t *testing.T, raw string, fn func(header, payload map[string]any), sig *string) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", raw)
	}
	var header, payload map[string]any
	for i, dst := range []*map[string]any{&header, &payload} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, dst); err != nil {
			t.Fatal(err)
		}
	}
	fn(header, payload)
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signature := parts[2]
	if sig != nil {
		signature = *sig
	}
	return enc(header) + "." + enc(payload) + "." + signature
}

func strPtr(s string) *string { return &s }

// Invariant: verify accepts only HS256 under our key. Tokens with the
// algorithm switched, no signature, or an asymmetric signature are refused
// even when every claim is right.
func TestVerifyRejectsForgedTokens(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	s := testSigner(clk)
	sub := uuid.New()
	raw, _, err := s.issue(sub, typDashboard, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.verify(raw, typDashboard); err != nil {
		t.Fatalf("control token rejected: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	claims := func() jwt.Token {
		tok, err := jwt.NewBuilder().Issuer(s.issuer).Subject(sub.String()).IssuedAt(clk.Now()).Expiration(clk.Now().Add(15*time.Minute)).
			Claim(claimTyp, typDashboard).Claim(claimIatMs, clk.Now().UnixMilli()).Build()
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	sign := func(opts ...jwt.SignOption) string {
		b, err := jwt.Sign(claims(), opts...)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	cases := map[string]string{
		"alg none (library)":        sign(jwt.WithInsecureNoSignature()),
		"alg none (hand-built)":     rewrite(t, raw, func(h, _ map[string]any) { h["alg"] = "none" }, strPtr("")),
		"alg none keeps signature":  rewrite(t, raw, func(h, _ map[string]any) { h["alg"] = "none" }, nil),
		"alg RS256 header only":     rewrite(t, raw, func(h, _ map[string]any) { h["alg"] = "RS256" }, nil),
		"RS256 signed":              sign(jwt.WithKey(jwa.RS256(), rsaKey)),
		"HS512 header only":         rewrite(t, raw, func(h, _ map[string]any) { h["alg"] = "HS512" }, nil),
		"HS256 other key":           sign(jwt.WithKey(jwa.HS256(), []byte("fedcba9876543210fedcba9876543210"))),
		"payload: other subject":    rewrite(t, raw, func(_, p map[string]any) { p["sub"] = uuid.NewString() }, nil),
		"payload: later expiry":     rewrite(t, raw, func(_, p map[string]any) { p["exp"] = clk.Now().Add(24 * time.Hour).Unix() }, nil),
		"payload: kind changed":     rewrite(t, raw, func(_, p map[string]any) { p[claimTyp] = typMFA }, nil),
		"payload: issuer changed":   rewrite(t, raw, func(_, p map[string]any) { p["iss"] = "evil" }, nil),
		"payload: one byte flipped": raw[:len(raw)/2] + string(raw[len(raw)/2]^1) + raw[len(raw)/2+1:],
		"signature stripped":        rewrite(t, raw, func(_, _ map[string]any) {}, strPtr("")),
		"signature truncated":       raw[:len(raw)-2],
		"kid injection":             rewrite(t, raw, func(h, _ map[string]any) { h["kid"] = "../../etc/passwd" }, nil),
		"JWE instead of JWS":        strings.Join([]string{"eyJhbGciOiJkaXIiLCJlbmMiOiJBMTI4R0NNIn0", "", "AAAAAAAAAAAAAAAA", "AAAA", "AAAAAAAAAAAAAAAAAAAAAA"}, "."),
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.verify(tok, typDashboard); err == nil {
				t.Error("forged token verified")
			}
		})
	}
}

// Invariant: the kind claim is enforced symmetrically and the challenge
// token expires after its TTL plus the skew.
func TestVerifyEnforcesKindAndMFATTL(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	s := testSigner(clk)
	sub := uuid.New()
	mfa, _, _ := s.issue(sub, typMFA, DefaultMFATTL)
	access, _, _ := s.issue(sub, typDashboard, DefaultAccessTTL)

	if _, err := s.verify(mfa, typDashboard); err == nil {
		t.Error("MFA challenge verified as a session")
	}
	if _, err := s.verify(access, typMFA); err == nil {
		t.Error("session verified as an MFA challenge")
	}
	if _, err := s.verify(mfa, typMFA); err != nil {
		t.Errorf("fresh challenge rejected: %v", err)
	}
	clk.Advance(DefaultMFATTL + acceptableSkew - time.Second)
	if _, err := s.verify(mfa, typMFA); err != nil {
		t.Errorf("challenge inside the skew rejected: %v", err)
	}
	clk.Advance(2 * time.Second)
	if _, err := s.verify(mfa, typMFA); err == nil {
		t.Error("expired challenge verified")
	}
	if _, err := s.verify(access, typDashboard); err != nil {
		t.Errorf("session still inside its TTL rejected: %v", err)
	}
}

// Invariant: a token must carry a subject that is a UUID and a kind claim;
// the millisecond issue time is preferred over the second-precision iat.
func TestVerifyRequiresSubjectAndKind(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 500_000_000, time.UTC))
	s := testSigner(clk)
	build := func(edit func(b *jwt.Builder)) string {
		b := jwt.NewBuilder().Issuer(s.issuer).IssuedAt(clk.Now()).Expiration(clk.Now().Add(time.Minute))
		edit(b)
		tok, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), s.key))
		if err != nil {
			t.Fatal(err)
		}
		return string(signed)
	}
	sub := uuid.New()
	bad := map[string]string{
		"no subject":   build(func(b *jwt.Builder) { b.Claim(claimTyp, typDashboard) }),
		"non-uuid":     build(func(b *jwt.Builder) { b.Subject("root").Claim(claimTyp, typDashboard) }),
		"no kind":      build(func(b *jwt.Builder) { b.Subject(sub.String()) }),
		"numeric kind": build(func(b *jwt.Builder) { b.Subject(sub.String()).Claim(claimTyp, 1) }),
		"no issuer":    build(func(b *jwt.Builder) { b.Issuer("").Subject(sub.String()).Claim(claimTyp, typDashboard) }),
		"not yet valid": build(func(b *jwt.Builder) {
			b.Subject(sub.String()).Claim(claimTyp, typDashboard).NotBefore(clk.Now().Add(time.Hour))
		}),
		"issued far ahead": build(func(b *jwt.Builder) {
			b.Subject(sub.String()).Claim(claimTyp, typDashboard).IssuedAt(clk.Now().Add(time.Hour))
		}),
	}
	for name, tok := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := s.verify(tok, typDashboard); err == nil {
				t.Error("token verified")
			}
		})
	}
	// Without iat_ms the second-precision iat is used; with it, milliseconds.
	plain := build(func(b *jwt.Builder) { b.Subject(sub.String()).Claim(claimTyp, typDashboard) })
	cl, err := s.verify(plain, typDashboard)
	if err != nil || !cl.IssuedAt.Equal(clk.Now().Truncate(time.Second)) {
		t.Errorf("iat fallback: %v %s", err, cl.IssuedAt)
	}
	precise := build(func(b *jwt.Builder) {
		b.Subject(sub.String()).Claim(claimTyp, typDashboard).Claim(claimIatMs, clk.Now().UnixMilli())
	})
	cl, err = s.verify(precise, typDashboard)
	if err != nil || !cl.IssuedAt.Equal(clk.Now().Truncate(time.Millisecond)) {
		t.Errorf("iat_ms: %v %s", err, cl.IssuedAt)
	}
}
