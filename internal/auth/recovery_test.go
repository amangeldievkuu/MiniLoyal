package auth

import (
	"regexp"
	"testing"
)

func TestRecoveryCodeShapeAndNormalization(t *testing.T) {
	code, err := newRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{5}-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{5}$`).MatchString(code) {
		t.Fatalf("unexpected code %q", code)
	}
	variants := []string{code, " " + code + " ", code[:5] + code[6:], code[:5] + " " + code[6:]}
	lower := []byte(code)
	for i := range lower {
		if lower[i] >= 'A' && lower[i] <= 'Z' {
			lower[i] += 'a' - 'A'
		}
	}
	variants = append(variants, string(lower))
	want := hashRecoveryCode(code)
	for _, v := range variants {
		if hashRecoveryCode(v) != want {
			t.Errorf("%q hashes differently from %q", v, code)
		}
	}
}

func TestLooksLikeTOTP(t *testing.T) {
	cases := map[string]bool{"123456": true, "12345": false, "1234567": false, "12345a": false, "ABCDE-FGHJK": false}
	for in, want := range cases {
		if got := looksLikeTOTP(in); got != want {
			t.Errorf("looksLikeTOTP(%q) = %v", in, got)
		}
	}
}
