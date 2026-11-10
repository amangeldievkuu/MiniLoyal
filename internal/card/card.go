// Package card implements the rotating member card and the scan token.
//
// The QR code a member's phone shows changes every 30 seconds:
//
//	ml1.<base64url(app_id[16] | member_id[16] | window[4] | mac[16])>
//
// window is unix time / 30 and mac is HMAC-SHA256 over a version tag, the
// app, the member and the window, truncated to 16 bytes. The MAC key is
// derived per member with HKDF from a server master key and the member's
// qr_key_version, so bumping the version invalidates every code that member
// ever showed, and no per-member secret has to be stored. A code is accepted
// for its own window and one either side, so a screenshot stops working
// after 60 to 90 seconds.
//
// Scanning a valid code gives the staff app a scan token: a 5-minute HMAC
// token binding the staff member, the member, the app and the tenant. Points
// and coupon actions require it, so staff can only act on a member whose
// card they actually scanned.
package card

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

const (
	// Window is how long one QR code is the current one.
	Window = 30 * time.Second
	// ScanTokenTTL is how long a scan token authorizes staff actions.
	ScanTokenTTL = 5 * time.Minute

	qrPrefix      = "ml1."
	qrVersionTag  = "miniloyal/member-qr/v1"
	scanPrefix    = "st1."
	scanKeyInfo   = "miniloyal/scan-token/v1"
	macLen        = 16
	rawLen        = 16 + 16 + 4 + macLen
	minMasterKey  = 32
	maxPayloadLen = 128
)

// Errors. Callers map them to one client-facing message.
var (
	ErrMalformed = errors.New("card: malformed code")
	ErrExpired   = errors.New("card: code expired")
	ErrInvalid   = errors.New("card: code does not verify")
)

// Codec issues and verifies QR payloads and scan tokens.
type Codec struct {
	master  []byte
	scanKey []byte
	clock   clock.Clock
}

// New returns a Codec. master is at least 32 bytes of secret key material
// (the API passes its session signing key).
func New(master []byte, clk clock.Clock) (*Codec, error) {
	if len(master) < minMasterKey {
		return nil, fmt.Errorf("card: master key must be at least %d bytes", minMasterKey)
	}
	if clk == nil {
		clk = clock.Real{}
	}
	scanKey, err := hkdf.Key(sha256.New, master, nil, scanKeyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("card: derive scan key: %w", err)
	}
	return &Codec{master: append([]byte(nil), master...), scanKey: scanKey, clock: clk}, nil
}

// Card is what GET /v1/me/card returns.
type Card struct {
	Payload   string
	ExpiresAt time.Time     // the last instant a scanner accepts this code
	RefreshIn time.Duration // when the phone should fetch the next code
}

// Card returns the current code for the member.
func (c *Codec) Card(appID, memberID uuid.UUID, keyVersion int) (Card, error) {
	now := c.clock.Now()
	w := windowOf(now)
	mac, err := c.mac(appID, memberID, keyVersion, w)
	if err != nil {
		return Card{}, err
	}
	raw := make([]byte, 0, rawLen)
	raw = append(raw, appID[:]...)
	raw = append(raw, memberID[:]...)
	raw = binary.BigEndian.AppendUint32(raw, uint32(w))
	raw = append(raw, mac...)
	start := time.Unix(w*int64(Window/time.Second), 0)
	return Card{
		Payload:   qrPrefix + base64.RawURLEncoding.EncodeToString(raw),
		ExpiresAt: start.Add(2 * Window),
		RefreshIn: start.Add(Window).Sub(now),
	}, nil
}

// Parsed is a QR payload whose shape is valid but whose MAC is not checked.
type Parsed struct {
	AppID    uuid.UUID
	MemberID uuid.UUID
	window   int64
	mac      []byte
}

// Parse decodes a payload. Verify must be called with the member's current
// key version before trusting it.
func Parse(payload string) (Parsed, error) {
	payload = strings.TrimSpace(payload)
	if len(payload) > maxPayloadLen || !strings.HasPrefix(payload, qrPrefix) {
		return Parsed{}, ErrMalformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload[len(qrPrefix):])
	if err != nil || len(raw) != rawLen {
		return Parsed{}, ErrMalformed
	}
	var p Parsed
	copy(p.AppID[:], raw[0:16])
	copy(p.MemberID[:], raw[16:32])
	p.window = int64(binary.BigEndian.Uint32(raw[32:36]))
	p.mac = raw[36:]
	return p, nil
}

// Verify checks the MAC under the member's current key version first, then
// the window (current one, or one either side). Checking the MAC first means
// ErrExpired is only ever returned for a code the server really issued, so
// the answer reveals nothing about member IDs a caller made up.
func (c *Codec) Verify(p Parsed, keyVersion int) error {
	want, err := c.mac(p.AppID, p.MemberID, keyVersion, p.window)
	if err != nil {
		return err
	}
	if !hmac.Equal(want, p.mac) {
		return ErrInvalid
	}
	now := windowOf(c.clock.Now())
	if d := now - p.window; d > 1 || d < -1 {
		return ErrExpired
	}
	return nil
}

func (c *Codec) mac(appID, memberID uuid.UUID, keyVersion int, window int64) ([]byte, error) {
	if keyVersion < 1 {
		return nil, fmt.Errorf("card: key version must be positive")
	}
	key, err := hkdf.Key(sha256.New, c.master, memberID[:], fmt.Sprintf("%s/%d", qrVersionTag, keyVersion), 32)
	if err != nil {
		return nil, fmt.Errorf("card: derive member key: %w", err)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(qrVersionTag))
	m.Write(appID[:])
	m.Write(memberID[:])
	var w [8]byte
	binary.BigEndian.PutUint64(w[:], uint64(window))
	m.Write(w[:])
	return m.Sum(nil)[:macLen], nil
}

func windowOf(t time.Time) int64 { return t.Unix() / int64(Window/time.Second) }

// ScanClaims bind a scan to one staff member and one member of one app.
// JTI identifies the scan so it can pay for at most one redemption, and
// KeyVersion is the member's card key version at scan time, so rotating the
// card also invalidates scans already made.
type ScanClaims struct {
	StaffID    uuid.UUID `json:"stf"`
	MemberID   uuid.UUID `json:"mem"`
	AppID      uuid.UUID `json:"app"`
	TenantID   uuid.UUID `json:"ten"`
	KeyVersion int       `json:"kv"`
	JTI        string    `json:"jti"`
	IssuedAt   int64     `json:"iat"`
	ExpiresAt  int64     `json:"exp"`
}

// Expired reports whether the claims' lifetime ended at now.
func (cl ScanClaims) Expired(now time.Time) bool { return now.Unix() >= cl.ExpiresAt }

// IssueScanToken returns a token for the claims, valid for ScanTokenTTL.
// keyVersion is the member's qr_key_version the scanned code verified under.
func (c *Codec) IssueScanToken(staffID, memberID, appID, tenantID uuid.UUID, keyVersion int) (string, time.Time, error) {
	now := c.clock.Now()
	exp := now.Add(ScanTokenTTL)
	var jti [16]byte
	if _, err := rand.Read(jti[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("card: scan token id: %w", err)
	}
	body, err := json.Marshal(ScanClaims{
		StaffID: staffID, MemberID: memberID, AppID: appID, TenantID: tenantID,
		KeyVersion: keyVersion, JTI: base64.RawURLEncoding.EncodeToString(jti[:]),
		IssuedAt: now.Unix(), ExpiresAt: exp.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	enc := base64.RawURLEncoding.EncodeToString(body)
	m := hmac.New(sha256.New, c.scanKey)
	m.Write([]byte(scanPrefix + enc))
	return scanPrefix + enc + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), exp, nil
}

// VerifyScanToken checks the MAC and expiry and returns the claims. The
// caller must still check StaffID against the signed-in staff member.
func (c *Codec) VerifyScanToken(token string) (ScanClaims, error) {
	cl, err := c.VerifyScanTokenIgnoringExpiry(token)
	if err != nil {
		return ScanClaims{}, err
	}
	if cl.Expired(c.clock.Now()) {
		return ScanClaims{}, ErrExpired
	}
	return cl, nil
}

// VerifyScanTokenIgnoringExpiry checks the MAC and the shape but not the
// lifetime. Points writes use it so an idempotent retry of a write that
// already happened still replays after the token ran out; the lifetime is
// checked before a write is made for the first time.
func (c *Codec) VerifyScanTokenIgnoringExpiry(token string) (ScanClaims, error) {
	if len(token) > 1024 || !strings.HasPrefix(token, scanPrefix) {
		return ScanClaims{}, ErrMalformed
	}
	enc, sig, ok := strings.Cut(token[len(scanPrefix):], ".")
	if !ok {
		return ScanClaims{}, ErrMalformed
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return ScanClaims{}, ErrMalformed
	}
	m := hmac.New(sha256.New, c.scanKey)
	m.Write([]byte(scanPrefix + enc))
	if !hmac.Equal(m.Sum(nil), got) {
		return ScanClaims{}, ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return ScanClaims{}, ErrMalformed
	}
	var cl ScanClaims
	if err := json.Unmarshal(body, &cl); err != nil {
		return ScanClaims{}, ErrMalformed
	}
	if cl.StaffID == uuid.Nil || cl.MemberID == uuid.Nil || cl.AppID == uuid.Nil || cl.TenantID == uuid.Nil || cl.KeyVersion < 1 || cl.JTI == "" {
		return ScanClaims{}, ErrMalformed
	}
	return cl, nil
}
