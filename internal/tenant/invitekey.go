package tenant

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// inviteKeyInfo separates the invite-code key from every other key derived
// from the session signing key.
const inviteKeyInfo = "miniloyal/staff-invite"

// devInviteSecret is used only when no signing key was set (unit tests that
// never sign staff in). cmd/api always calls SetInviteKey.
var devInviteSecret = []byte("miniloyal-development-only-invite-key!!!")

// InviteKey derives the invite-code HMAC key from the session signing key.
// The staff sign-in service derives the same key to look codes up.
func InviteKey(signingKey []byte) []byte {
	k, err := hkdf.Key(sha256.New, signingKey, nil, inviteKeyInfo, 32)
	if err != nil {
		panic("tenant: derive invite key: " + err.Error())
	}
	return k
}

// SetInviteKey sets the key staff invite codes are hashed with. Pass the
// session signing key; the service derives its own key from it.
func (s *Service) SetInviteKey(signingKey []byte) { s.inviteKey = InviteKey(signingKey) }

// HashInviteCodeWithKey normalizes a code as typed by a person (case, spaces
// and dashes do not matter) and returns its stored form: HMAC-SHA256 under
// key (from InviteKey), so a leaked table does not allow an offline search.
func HashInviteCodeWithKey(key []byte, code string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(NormalizeInviteCode(code)))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) hashInvite(code string) string {
	key := s.inviteKey
	if key == nil {
		key = InviteKey(devInviteSecret)
	}
	return HashInviteCodeWithKey(key, code)
}
