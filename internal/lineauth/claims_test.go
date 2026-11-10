package lineauth

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwt"
)

// Invariant: iat_ms is read whatever numeric form the JSON decoder chose,
// and anything that is not a whole number of milliseconds is no claim, so
// the member session check against member_sessions_valid_from never runs
// on a guessed time.
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
