package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP's second recommended configuration).
const (
	argonTime    = 2
	argonMemory  = 19456 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Password length policy.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 200
)

var errBadHash = errors.New("auth: malformed password hash")

// HashPassword derives an argon2id hash and encodes it as a PHC string:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC-encoded hash. The
// comparison is constant time; the parameters come from the hash so old
// hashes keep verifying after the defaults change.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, errBadHash
	}
	// Parameters come from the stored hash; refuse values that would exhaust
	// memory or panic if a row were ever tampered with.
	if memory < 8*1024 || memory > 1<<20 || iterations < 1 || iterations > 16 || threads < 1 || threads > 16 {
		return false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// validPassword applies the length policy.
func validPassword(password string) string {
	switch n := len(password); {
	case n < MinPasswordLen:
		return fmt.Sprintf("must be at least %d characters", MinPasswordLen)
	case n > MaxPasswordLen:
		return fmt.Sprintf("must be at most %d characters", MaxPasswordLen)
	}
	return ""
}
