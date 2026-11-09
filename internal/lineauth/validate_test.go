package lineauth

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Invariant: the sign-in request is bounded before any token is parsed. An
// ID token is accepted between 20 and 4096 bytes after trimming; the LIFF
// access token, nonce and language have their own caps; a blank language
// is dropped rather than stored.
func TestValidateLoginBounds(t *testing.T) {
	app := uuid.New()
	tok := func(n int) string { return strings.Repeat("x", n) }
	str := func(s string) *string { return &s }

	cases := []struct {
		name  string
		in    LoginInput
		field string // empty = valid
	}{
		{"shortest token", LoginInput{AppID: app, IDToken: tok(minIDTokenLen)}, ""},
		{"one byte short", LoginInput{AppID: app, IDToken: tok(minIDTokenLen - 1)}, "id_token"},
		{"longest token", LoginInput{AppID: app, IDToken: tok(maxIDTokenLen)}, ""},
		{"one byte over", LoginInput{AppID: app, IDToken: tok(maxIDTokenLen + 1)}, "id_token"},
		{"whitespace around the longest token", LoginInput{AppID: app, IDToken: "  " + tok(maxIDTokenLen) + "\n"}, ""},
		{"only whitespace", LoginInput{AppID: app, IDToken: strings.Repeat(" ", 100)}, "id_token"},
		{"megabyte token", LoginInput{AppID: app, IDToken: tok(1 << 20)}, "id_token"},
		{"missing app", LoginInput{IDToken: tok(100)}, "app_id"},
		{"nonce at the cap", LoginInput{AppID: app, IDToken: tok(100), Nonce: tok(255)}, ""},
		{"nonce over the cap", LoginInput{AppID: app, IDToken: tok(100), Nonce: tok(256)}, "nonce"},
		{"access token at the cap", LoginInput{AppID: app, IDToken: tok(100), LiffAccessToken: tok(maxAccessTokenLen)}, ""},
		{"access token over the cap", LoginInput{AppID: app, IDToken: tok(100), LiffAccessToken: tok(maxAccessTokenLen + 1)}, "liff_access_token"},
		{"language at the cap", LoginInput{AppID: app, IDToken: tok(100), Language: str(tok(maxLanguageLen))}, ""},
		{"language over the cap", LoginInput{AppID: app, IDToken: tok(100), Language: str(tok(maxLanguageLen + 1))}, "language"},
		{"blank language", LoginInput{AppID: app, IDToken: tok(100), Language: str("   ")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			fields := validateLogin(&in)
			if tc.field == "" {
				if len(fields) != 0 {
					t.Fatalf("unexpected validation errors: %v", fields)
				}
				if strings.TrimSpace(tc.in.IDToken) != in.IDToken {
					t.Errorf("id_token not trimmed: %q", in.IDToken)
				}
				if tc.in.Language != nil && strings.TrimSpace(*tc.in.Language) == "" && in.Language != nil {
					t.Errorf("blank language kept: %q", *in.Language)
				}
				return
			}
			if fields[tc.field] == "" {
				t.Fatalf("want an error on %s, got %v", tc.field, fields)
			}
		})
	}
}
