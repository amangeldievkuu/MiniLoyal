package auth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Recovery codes: 10 characters from an alphabet without look-alikes, shown
// as XXXXX-XXXXX. They carry about 50 bits of entropy, so the stored form is
// an HMAC under a server key rather than a plain hash: a database dump alone
// cannot be brute-forced back into working codes.
const (
	recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	recoveryCodeLen  = 10
	recoveryCodes    = 8

	recoveryKeyInfo = "miniloyal/recovery-codes"
)

func newRecoveryCode() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(recoveryAlphabet)))
	for i := 0; i < recoveryCodeLen; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("auth: recovery code: %w", err)
		}
		b.WriteByte(recoveryAlphabet[n.Int64()])
	}
	code := b.String()
	return code[:5] + "-" + code[5:], nil
}

// normalizeRecoveryCode accepts the code however the user typed it.
func normalizeRecoveryCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", " ", "").Replace(s)
}

// deriveRecoveryKey derives the HMAC key for recovery codes from the session
// signing key, so one configured secret protects both.
func deriveRecoveryKey(signingKey []byte) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, signingKey, nil, recoveryKeyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("auth: derive recovery key: %w", err)
	}
	return key, nil
}

// hashRecoveryCodeWith is the stored form of a code under key.
func hashRecoveryCodeWith(key []byte, code string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(normalizeRecoveryCode(code)))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) hashRecoveryCode(code string) string {
	return hashRecoveryCodeWith(s.recoveryKey, code)
}

// looksLikeTOTP reports whether code is a six-digit one-time password.
func looksLikeTOTP(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
