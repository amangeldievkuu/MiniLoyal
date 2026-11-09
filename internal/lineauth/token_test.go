package lineauth

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

var testKey = []byte("member-session-test-signing-key!")

func TestSessionTokenRoundTrip(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	s := &signer{key: testKey, issuer: DefaultIssuer, clock: clk}
	want := sessionClaims{MemberID: uuid.New(), TenantID: uuid.New(), AppID: uuid.New()}

	raw, err := s.issue(want, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemberID != want.MemberID || got.TenantID != want.TenantID || got.AppID != want.AppID {
		t.Errorf("claims = %+v, want %+v", got, want)
	}
	if !got.IssuedAt.Equal(clk.Now()) {
		t.Errorf("issued at %s, want %s", got.IssuedAt, clk.Now())
	}

	clk.Advance(time.Hour + 31*time.Second)
	if _, err := s.verify(raw); err == nil {
		t.Error("expired token accepted")
	}
}

func TestSessionTokenRejectsForgeries(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	s := &signer{key: testKey, issuer: DefaultIssuer, clock: clk}
	member, tenant, app := uuid.New(), uuid.New(), uuid.New()

	build := func(issuer, typ string, key []byte, claims map[string]any) string {
		t.Helper()
		b := jwt.NewBuilder().Issuer(issuer).Subject(member.String()).IssuedAt(clk.Now()).Expiration(clk.Now().Add(time.Hour))
		for k, v := range claims {
			b = b.Claim(k, v)
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
	full := map[string]any{claimTyp: typMember, claimTenantID: tenant.String(), claimAppID: app.String()}

	cases := map[string]string{
		"dashboard kind":   build(DefaultIssuer, typMember, testKey, map[string]any{claimTyp: "dashboard", claimTenantID: tenant.String(), claimAppID: app.String()}),
		"missing kind":     build(DefaultIssuer, typMember, testKey, map[string]any{claimTenantID: tenant.String(), claimAppID: app.String()}),
		"missing tenant":   build(DefaultIssuer, typMember, testKey, map[string]any{claimTyp: typMember, claimAppID: app.String()}),
		"bad app id":       build(DefaultIssuer, typMember, testKey, map[string]any{claimTyp: typMember, claimTenantID: tenant.String(), claimAppID: "nope"}),
		"other issuer":     build("someone-else", typMember, testKey, full),
		"other key":        build(DefaultIssuer, typMember, []byte("another-32-byte-key-for-signing!"), full),
		"garbage":          "not.a.token",
		"truncated":        build(DefaultIssuer, typMember, testKey, full)[:40],
		"empty":            "",
		"tampered payload": tamper(build(DefaultIssuer, typMember, testKey, full)),
	}
	for name, raw := range cases {
		if _, err := s.verify(raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := s.verify(build(DefaultIssuer, typMember, testKey, full)); err != nil {
		t.Errorf("control token rejected: %v", err)
	}
}

// tamper flips one byte in the payload segment.
func tamper(raw string) string {
	b := []byte(raw)
	first := 0
	for i, c := range b {
		if c == '.' {
			first = i
			break
		}
	}
	b[first+2] ^= 0x01
	return string(b)
}
