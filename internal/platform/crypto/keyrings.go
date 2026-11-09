package crypto

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

// ErrKMSNotConfigured is returned until the Cloud KMS wrapper lands (Phase 7).
var ErrKMSNotConfigured = errors.New("crypto: Cloud KMS keyring is not implemented yet; use KEK_FILE")

// NewLocalKeyring reads a base64-encoded 32-byte key-encryption key from
// path. With createIfMissing (development only) a missing file is created
// with a fresh random key and mode 0600.
func NewLocalKeyring(path string, createIfMissing bool) (Keyring, error) {
	if path == "" {
		return nil, errors.New("crypto: KEK_FILE is empty")
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist) && createIfMissing:
		raw, err = createKEKFile(path)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("crypto: read KEK_FILE: %w", err)
	}
	kek, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("crypto: KEK_FILE is not base64: %w", err)
	}
	return NewStaticKeyring(kek)
}

func createKEKFile(path string) ([]byte, error) {
	kek := make([]byte, dekSize)
	if _, err := rand.Read(kek); err != nil {
		return nil, fmt.Errorf("crypto: generate KEK: %w", err)
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(kek) + "\n")
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("crypto: create KEK directory: %w", err)
		}
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("crypto: write KEK_FILE: %w", err)
	}
	return encoded, nil
}

// NewKMSKeyring will wrap data keys with a Cloud KMS key. Not available yet.
func NewKMSKeyring(keyName string) (Keyring, error) {
	if keyName == "" {
		return nil, errors.New("crypto: KMS_KEY_NAME is empty")
	}
	return nil, ErrKMSNotConfigured
}

// FromConfig picks the keyring the configuration asks for: Cloud KMS when
// KMS_KEY_NAME is set, otherwise the local KEK file, which is created on
// first use in development only.
func FromConfig(cfg *config.Config) (Keyring, error) {
	if cfg.KMSKeyName != "" {
		return NewKMSKeyring(cfg.KMSKeyName)
	}
	return NewLocalKeyring(cfg.KEKFile, cfg.Env == config.Development)
}

// Static returns a Keyring from an in-memory key, failing the caller on a
// bad key. Meant for tests.
func Static(kek []byte) Keyring {
	k, err := NewStaticKeyring(kek)
	if err != nil {
		panic(err)
	}
	return k
}

// Ensure envelope satisfies Keyring with a context-aware signature.
var _ Keyring = (*envelope)(nil)

// Noop returns a Keyring that fails every call; it exists so wiring code can
// make the dependency explicit when encryption is intentionally unavailable.
func Noop() Keyring { return noopKeyring{} }

type noopKeyring struct{}

func (noopKeyring) Encrypt(context.Context, []byte, string) ([]byte, error) {
	return nil, errors.New("crypto: no keyring configured")
}

func (noopKeyring) Decrypt(context.Context, []byte, string) ([]byte, error) {
	return nil, errors.New("crypto: no keyring configured")
}
