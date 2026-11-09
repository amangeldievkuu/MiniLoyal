// Package crypto protects secrets at rest with envelope encryption: every
// value gets its own random data key (AES-256-GCM), and the data key is
// wrapped by a key-encryption key that lives in a local file (development)
// or in Cloud KMS (production, Phase 7).
//
// Ciphertext layout (version 1):
//
//	0x01 | len(wrapped DEK) as 2 bytes big-endian | wrapped DEK | 12-byte nonce | AES-GCM ciphertext
//
// The additional authenticated data binds a ciphertext to its row, by
// convention "<table>.<column>:<row key>", so a ciphertext copied to another
// row fails to decrypt.
package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// Keyring encrypts and decrypts secrets.
type Keyring interface {
	Encrypt(ctx context.Context, plaintext []byte, aad string) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte, aad string) ([]byte, error)
}

// ErrInvalidCiphertext is returned for malformed, tampered or foreign data.
var ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")

const (
	versionByte = 0x01
	dekSize     = 32
	nonceSize   = 12
)

// wrapper wraps and unwraps data keys with the key-encryption key.
type wrapper interface {
	wrap(ctx context.Context, dek []byte) ([]byte, error)
	unwrap(ctx context.Context, wrapped []byte) ([]byte, error)
}

// envelope implements Keyring on top of a wrapper.
type envelope struct {
	kek wrapper
}

func (e *envelope) Encrypt(ctx context.Context, plaintext []byte, aad string) ([]byte, error) {
	dek := make([]byte, dekSize)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("crypto: data key: %w", err)
	}
	wrapped, err := e.kek.wrap(ctx, dek)
	if err != nil {
		return nil, err
	}
	if len(wrapped) > 0xFFFF {
		return nil, errors.New("crypto: wrapped key too large")
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(aad))

	out := make([]byte, 0, 1+2+len(wrapped)+nonceSize+len(sealed))
	out = append(out, versionByte)
	out = binary.BigEndian.AppendUint16(out, uint16(len(wrapped)))
	out = append(out, wrapped...)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

func (e *envelope) Decrypt(ctx context.Context, ciphertext []byte, aad string) ([]byte, error) {
	if len(ciphertext) < 1+2 || ciphertext[0] != versionByte {
		return nil, ErrInvalidCiphertext
	}
	wrappedLen := int(binary.BigEndian.Uint16(ciphertext[1:3]))
	rest := ciphertext[3:]
	if len(rest) < wrappedLen+nonceSize {
		return nil, ErrInvalidCiphertext
	}
	wrapped := rest[:wrappedLen]
	nonce := rest[wrappedLen : wrappedLen+nonceSize]
	sealed := rest[wrappedLen+nonceSize:]

	dek, err := e.kek.unwrap(ctx, wrapped)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, sealed, []byte(aad))
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	return gcm, nil
}

// aesWrapper wraps data keys with a raw 32-byte KEK held in memory.
type aesWrapper struct {
	gcm cipher.AEAD
}

func newAESWrapper(kek []byte) (*aesWrapper, error) {
	if len(kek) != dekSize {
		return nil, fmt.Errorf("crypto: key-encryption key must be %d bytes, got %d", dekSize, len(kek))
	}
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	return &aesWrapper{gcm: gcm}, nil
}

func (w *aesWrapper) wrap(_ context.Context, dek []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	return append(nonce, w.gcm.Seal(nil, nonce, dek, nil)...), nil
}

func (w *aesWrapper) unwrap(_ context.Context, wrapped []byte) ([]byte, error) {
	if len(wrapped) < nonceSize {
		return nil, ErrInvalidCiphertext
	}
	dek, err := w.gcm.Open(nil, wrapped[:nonceSize], wrapped[nonceSize:], nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return dek, nil
}

// NewStaticKeyring builds a Keyring from a raw 32-byte key held in memory.
// Tests and the local file keyring use it.
func NewStaticKeyring(kek []byte) (Keyring, error) {
	w, err := newAESWrapper(kek)
	if err != nil {
		return nil, err
	}
	return &envelope{kek: w}, nil
}
