package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Recovery codes: 10 characters from an alphabet without look-alikes,
// shown as XXXXX-XXXXX and stored as SHA-256 hex of the normalized form.
const (
	recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	recoveryCodeLen  = 10
	recoveryCodes    = 8
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

// hashRecoveryCode is the stored form.
func hashRecoveryCode(s string) string {
	sum := sha256.Sum256([]byte(normalizeRecoveryCode(s)))
	return hex.EncodeToString(sum[:])
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
