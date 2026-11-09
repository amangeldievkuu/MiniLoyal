package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

var testRecoveryKey = []byte("recovery-key-for-tests-32-bytes!")

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
	want := hashRecoveryCodeWith(testRecoveryKey, code)
	for _, v := range variants {
		if hashRecoveryCodeWith(testRecoveryKey, v) != want {
			t.Errorf("%q hashes differently from %q", v, code)
		}
	}
}

// The stored form is an HMAC under a server key, not a bare hash: a copy of
// the table alone cannot be brute-forced back into codes.
func TestRecoveryCodeHashIsKeyed(t *testing.T) {
	code := "ABCDE-FGHJK"
	plain := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
	keyed := hashRecoveryCodeWith(testRecoveryKey, code)
	if keyed == hex.EncodeToString(plain[:]) {
		t.Fatal("stored form equals the plain SHA-256 of the code")
	}
	other := hashRecoveryCodeWith([]byte("another-key-for-tests-32-bytes!!"), code)
	if other == keyed {
		t.Fatal("different keys produced the same hash")
	}
	if len(keyed) != 64 {
		t.Errorf("hash length = %d, want 64 hex characters", len(keyed))
	}
}

func TestDeriveRecoveryKeyIsDeterministicAndDistinct(t *testing.T) {
	signing := []byte("integration-test-signing-key-32b!")
	a, err := deriveRecoveryKey(signing)
	if err != nil {
		t.Fatal(err)
	}
	b, err := deriveRecoveryKey(signing)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || len(a) != 32 {
		t.Fatalf("derivation is not deterministic or not 32 bytes: %x %x", a, b)
	}
	if bytes.Equal(a, signing) {
		t.Fatal("the recovery key must not be the signing key itself")
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
