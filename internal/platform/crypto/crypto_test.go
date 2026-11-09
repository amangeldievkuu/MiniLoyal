package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

func testKey(t *testing.T) Keyring {
	t.Helper()
	kek := bytes.Repeat([]byte{7}, 32)
	k, err := NewStaticKeyring(kek)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	k := testKey(t)
	ctx := context.Background()
	cases := map[string][]byte{
		"short":  []byte("channel-secret"),
		"empty":  {},
		"binary": {0, 1, 2, 255, 254},
		"long":   bytes.Repeat([]byte("x"), 4096),
	}
	for name, plaintext := range cases {
		t.Run(name, func(t *testing.T) {
			ct, err := k.Encrypt(ctx, plaintext, "apps.oa_channel_secret_enc:row")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(ct, plaintext) && len(plaintext) > 0 {
				t.Fatal("ciphertext contains the plaintext")
			}
			got, err := k.Decrypt(ctx, ct, "apps.oa_channel_secret_enc:row")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Fatalf("got %q, want %q", got, plaintext)
			}
		})
	}
}

func TestEncryptIsRandomised(t *testing.T) {
	k := testKey(t)
	a, _ := k.Encrypt(context.Background(), []byte("same"), "aad")
	b, _ := k.Encrypt(context.Background(), []byte("same"), "aad")
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same value must differ")
	}
}

func TestDecryptFailures(t *testing.T) {
	k := testKey(t)
	ctx := context.Background()
	ct, err := k.Encrypt(ctx, []byte("secret"), "table.col:1")
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(ct)
	tampered[len(tampered)-1] ^= 0x01
	tamperedKey := bytes.Clone(ct)
	tamperedKey[5] ^= 0x01
	otherKEK, _ := NewStaticKeyring(bytes.Repeat([]byte{9}, 32))

	cases := []struct {
		name string
		k    Keyring
		ct   []byte
		aad  string
	}{
		{"wrong aad", k, ct, "table.col:2"},
		{"tampered body", k, tampered, "table.col:1"},
		{"tampered wrapped key", k, tamperedKey, "table.col:1"},
		{"other kek", otherKEK, ct, "table.col:1"},
		{"truncated", k, ct[:10], "table.col:1"},
		{"empty", k, nil, "table.col:1"},
		{"bad version", k, append([]byte{0x02}, ct[1:]...), "table.col:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.k.Decrypt(ctx, tc.ct, tc.aad); !errors.Is(err, ErrInvalidCiphertext) {
				t.Fatalf("got %v, want ErrInvalidCiphertext", err)
			}
		})
	}
}

func TestStaticKeyringRejectsBadKeySize(t *testing.T) {
	if _, err := NewStaticKeyring([]byte("short")); err == nil {
		t.Fatal("expected an error for a 5-byte key")
	}
}

func TestLocalKeyringCreatesAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", ".kek")
	if _, err := NewLocalKeyring(path, false); err == nil {
		t.Fatal("missing file must fail without createIfMissing")
	}
	k1, err := NewLocalKeyring(path, true)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	ct, err := k1.Encrypt(context.Background(), []byte("v"), "a")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := NewLocalKeyring(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k2.Decrypt(context.Background(), ct, "a"); err != nil || string(got) != "v" {
		t.Fatalf("reloaded keyring: %q, %v", got, err)
	}
}

func TestLocalKeyringRejectsBadContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kek")
	_ = os.WriteFile(path, []byte("not base64!"), 0o600)
	if _, err := NewLocalKeyring(path, false); err == nil {
		t.Fatal("expected an error for non-base64 content")
	}
	_ = os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString([]byte("too short"))), 0o600)
	if _, err := NewLocalKeyring(path, false); err == nil {
		t.Fatal("expected an error for a short key")
	}
}

func TestFromConfig(t *testing.T) {
	if _, err := FromConfig(&config.Config{KMSKeyName: "projects/x/keys/y"}); !errors.Is(err, ErrKMSNotConfigured) {
		t.Fatalf("KMS: got %v", err)
	}
	path := filepath.Join(t.TempDir(), ".kek")
	if _, err := FromConfig(&config.Config{Env: config.Production, KEKFile: path}); err == nil {
		t.Fatal("production must not create a missing KEK")
	}
	if _, err := FromConfig(&config.Config{Env: config.Development, KEKFile: path}); err != nil {
		t.Fatalf("development should create the KEK: %v", err)
	}
}
