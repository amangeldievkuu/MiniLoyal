package staffauth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

var testKey = []byte("staff-session-test-signing-key-32!")

func TestTokenRoundTrip(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 10, 9, 0, 0, 123_000_000, time.UTC))
	s := &signer{key: testKey, issuer: DefaultIssuer, clock: clk}
	in := sessionClaims{StaffID: uuid.New(), TenantID: uuid.New(), AppID: uuid.New()}
	tok, err := s.issue(in, DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.verify(tok)
	if err != nil || got.StaffID != in.StaffID || got.TenantID != in.TenantID || got.AppID != in.AppID {
		t.Fatalf("verify: %+v %v", got, err)
	}
	if !got.IssuedAt.Equal(clk.Now().Truncate(time.Millisecond)) {
		t.Errorf("issued at %s, want millisecond precision of %s", got.IssuedAt, clk.Now())
	}

	clk.Advance(DefaultSessionTTL + time.Minute)
	if _, err := s.verify(tok); err == nil {
		t.Error("expired token accepted")
	}
}

func TestTokenRejectsOtherKindsAndForgeries(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
	s := &signer{key: testKey, issuer: DefaultIssuer, clock: clk}
	build := func(typ string, key []byte, issuer string) string {
		t.Helper()
		b := jwt.NewBuilder().Issuer(issuer).Subject(uuid.NewString()).IssuedAt(clk.Now()).Expiration(clk.Now().Add(time.Hour)).
			Claim("tenant_id", uuid.NewString()).Claim("app_id", uuid.NewString()).Claim("iat_ms", clk.Now().UnixMilli())
		if typ != "" {
			b = b.Claim("typ", typ)
		}
		tok, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), key))
		if err != nil {
			t.Fatal(err)
		}
		return string(signed)
	}
	valid := build("staff", testKey, DefaultIssuer)
	if _, err := s.verify(valid); err != nil {
		t.Fatalf("control: %v", err)
	}
	parts := strings.Split(valid, ".")
	for name, tok := range map[string]string{
		"member token":    build("member", testKey, DefaultIssuer),
		"dashboard token": build("dashboard", testKey, DefaultIssuer),
		"no typ":          build("", testKey, DefaultIssuer),
		"other key":       build("staff", []byte("another-signing-key-of-32-bytes!!"), DefaultIssuer),
		"other issuer":    build("staff", testKey, "someone-else"),
		"alg none":        "eyJhbGciOiJub25lIn0." + parts[1] + ".",
		"tampered body":   parts[0] + "." + parts[1] + "x." + parts[2],
		"garbage":         "not-a-token",
	} {
		if _, err := s.verify(tok); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
