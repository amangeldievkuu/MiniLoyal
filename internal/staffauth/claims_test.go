package staffauth

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// Invariant: iat_ms is read whatever numeric form the JSON decoder chose,
// and anything that is not a whole number of milliseconds is no claim.
func TestIssuedAtMsAcceptsNumbersOnly(t *testing.T) {
	const ms = 1_794_300_000_123
	cases := []struct {
		name string
		v    any
		ok   bool
	}{
		{"int64", int64(ms), true},
		{"int", int(ms), true},
		{"float64", float64(ms), true},
		{"json.Number", json.Number("1794300000123"), true},
		{"json.Number fraction", json.Number("1.5"), false},
		{"string", "1794300000123", false},
		{"bool", true, false},
		{"missing", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := jwt.NewBuilder()
			if tc.v != nil {
				b = b.Claim(claimIatMs, tc.v)
			}
			tok, err := b.Build()
			if err != nil {
				t.Fatal(err)
			}
			got, ok := issuedAtMs(tok)
			if ok != tc.ok || (ok && !got.Equal(time.UnixMilli(ms))) {
				t.Fatalf("issuedAtMs(%v) = %s, %v", tc.v, got, ok)
			}
		})
	}
}

// Invariant: a well-signed staff token missing any claim the session needs,
// or carrying one that is not a UUID, is refused.
func TestTokenNeedsEveryClaim(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
	s := &signer{key: testKey, issuer: DefaultIssuer, clock: clk}
	full := map[string]any{"sub": uuid.NewString(), claimTyp: typStaff, claimTenantID: uuid.NewString(), claimAppID: uuid.NewString(), claimIatMs: clk.Now().UnixMilli()}
	build := func(drop string, override map[string]any) string {
		t.Helper()
		b := jwt.NewBuilder().Issuer(DefaultIssuer).IssuedAt(clk.Now()).Expiration(clk.Now().Add(time.Hour))
		for k, v := range full {
			if k == drop {
				continue
			}
			if o, ok := override[k]; ok {
				v = o
			}
			if k == "sub" {
				b = b.Subject(v.(string))
				continue
			}
			b = b.Claim(k, v)
		}
		tok, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), testKey))
		if err != nil {
			t.Fatal(err)
		}
		return string(signed)
	}
	if _, err := s.verify(build("", nil)); err != nil {
		t.Fatalf("control: %v", err)
	}
	for name, tok := range map[string]string{
		"no subject":        build("sub", nil),
		"subject not uuid":  build("", map[string]any{"sub": "staff-1"}),
		"no tenant":         build(claimTenantID, nil),
		"tenant not uuid":   build("", map[string]any{claimTenantID: "tenant"}),
		"no app":            build(claimAppID, nil),
		"app not uuid":      build("", map[string]any{claimAppID: 42}),
		"no iat_ms":         build(claimIatMs, nil),
		"iat_ms not number": build("", map[string]any{claimIatMs: "now"}),
	} {
		if _, err := s.verify(tok); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Invariant: the service refuses to start without its collaborators or with
// a short signing key, and fills in the documented defaults.
func TestNewValidatesDependencies(t *testing.T) {
	codec, err := card.New(bytes.Repeat([]byte{1}, 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	ok := Deps{Pool: &pgxpool.Pool{}, LineAuth: &lineauth.Service{}, Card: codec, Config: Config{SigningKey: testKey}}
	for name, mutate := range map[string]func(d *Deps){
		"no pool":       func(d *Deps) { d.Pool = nil },
		"no line auth":  func(d *Deps) { d.LineAuth = nil },
		"no card codec": func(d *Deps) { d.Card = nil },
		"short key":     func(d *Deps) { d.Config.SigningKey = testKey[:31] },
	} {
		d := ok
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
	s, err := New(ok)
	if err != nil {
		t.Fatal(err)
	}
	if s.sessionTTL != DefaultSessionTTL || s.signer.issuer != DefaultIssuer || s.clock == nil || s.log == nil {
		t.Errorf("defaults not applied: ttl %s issuer %q", s.sessionTTL, s.signer.issuer)
	}
}
