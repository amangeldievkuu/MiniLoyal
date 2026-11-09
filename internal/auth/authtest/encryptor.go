// Package authtest holds test doubles for the auth package.
package authtest

import (
	"context"
	"errors"
	"strings"
)

// PlainEncryptor is a reversible stand-in for the platform keyring. It binds
// the ciphertext to the AAD like the real one, without any secrecy.
type PlainEncryptor struct{}

// Encrypt prefixes the plaintext with the AAD.
func (PlainEncryptor) Encrypt(_ context.Context, plaintext []byte, aad string) ([]byte, error) {
	return []byte("plain:" + aad + "|" + string(plaintext)), nil
}

// Decrypt checks the AAD and strips the prefix.
func (PlainEncryptor) Decrypt(_ context.Context, ciphertext []byte, aad string) ([]byte, error) {
	prefix := "plain:" + aad + "|"
	s := string(ciphertext)
	if !strings.HasPrefix(s, prefix) {
		return nil, errors.New("authtest: ciphertext does not match the AAD")
	}
	return []byte(strings.TrimPrefix(s, prefix)), nil
}
