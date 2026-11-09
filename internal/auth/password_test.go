package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected PHC prefix: %s", hash)
	}
	ok, err := VerifyPassword(hash, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong horse battery")
	if err != nil || ok {
		t.Fatalf("wrong password verified: %v, %v", ok, err)
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same password here")
	b, _ := HashPassword("same password here")
	if a == b {
		t.Error("two hashes of one password must differ")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$abc$def", "$argon2id$v=18$m=1,t=1,p=1$abc$def", "$argon2id$v=19$m=1,t=1,p=1$!!$def"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	if validPassword("short") == "" {
		t.Error("short password accepted")
	}
	if validPassword(strings.Repeat("x", 201)) == "" {
		t.Error("long password accepted")
	}
	if msg := validPassword("exactly10!"); msg != "" {
		t.Errorf("10 characters rejected: %s", msg)
	}
}
