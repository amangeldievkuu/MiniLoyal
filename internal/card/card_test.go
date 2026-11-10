package card

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

var master = bytes.Repeat([]byte{9}, 32)

func codec(t *testing.T, clk clock.Clock) *Codec {
	t.Helper()
	c, err := New(master, clk)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsShortKeys(t *testing.T) {
	if _, err := New(make([]byte, 31), nil); err == nil {
		t.Fatal("31-byte master key accepted")
	}
}

func TestCardRoundTripAndWindows(t *testing.T) {
	// Start exactly at a window boundary so the arithmetic is easy to read.
	start := time.Unix(1_800_000_000-1_800_000_000%30, 0)
	clk := clock.At(start.Add(5 * time.Second))
	c := codec(t, clk)
	app, member := uuid.New(), uuid.New()

	cd, err := c.Card(app, member, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cd.Payload, "ml1.") || len(cd.Payload) > maxPayloadLen {
		t.Fatalf("payload shape: %q", cd.Payload)
	}
	if cd.RefreshIn != 25*time.Second || !cd.ExpiresAt.Equal(start.Add(60*time.Second)) {
		t.Errorf("refresh %s, expires %s", cd.RefreshIn, cd.ExpiresAt)
	}
	p, err := Parse(cd.Payload)
	if err != nil || p.AppID != app || p.MemberID != member {
		t.Fatalf("parse: %+v %v", p, err)
	}

	cases := []struct {
		name    string
		advance time.Duration
		want    error
	}{
		{"same window", 0, nil},
		{"next window", 30 * time.Second, nil},
		{"two windows later (screenshot)", 30 * time.Second, ErrExpired},
	}
	for _, tc := range cases {
		clk.Advance(tc.advance)
		if err := c.Verify(p, 1); !errors.Is(err, tc.want) && err != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestPreviousWindowIsAccepted(t *testing.T) {
	clk := clock.At(time.Unix(1_800_000_015, 0))
	c := codec(t, clk)
	app, member := uuid.New(), uuid.New()
	clk.Advance(-30 * time.Second)
	cd, _ := c.Card(app, member, 1)
	clk.Advance(30 * time.Second)
	p, _ := Parse(cd.Payload)
	if err := c.Verify(p, 1); err != nil {
		t.Fatalf("code from the previous window: %v", err)
	}
	// A code two windows ahead of the scanner clock (skew) is refused.
	clk.Advance(-90 * time.Second)
	if err := c.Verify(p, 1); !errors.Is(err, ErrExpired) {
		t.Fatalf("code two windows ahead: %v", err)
	}
}

func TestKeyVersionRotationInvalidatesCodes(t *testing.T) {
	c := codec(t, clock.At(time.Unix(1_800_000_000, 0)))
	app, member := uuid.New(), uuid.New()
	cd, _ := c.Card(app, member, 1)
	p, _ := Parse(cd.Payload)
	if err := c.Verify(p, 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old code after rotation: %v", err)
	}
	if _, err := c.Card(app, member, 0); err == nil {
		t.Fatal("version 0 accepted")
	}
}

func TestTamperedPayloadsAreRefused(t *testing.T) {
	c := codec(t, clock.At(time.Unix(1_800_000_000, 0)))
	app, member := uuid.New(), uuid.New()
	cd, _ := c.Card(app, member, 1)
	raw, _ := base64.RawURLEncoding.DecodeString(cd.Payload[4:])

	flip := func(i int) string {
		b := append([]byte(nil), raw...)
		b[i] ^= 1
		return "ml1." + base64.RawURLEncoding.EncodeToString(b)
	}
	other, _ := c.Card(uuid.New(), member, 1)
	otherRaw, _ := base64.RawURLEncoding.DecodeString(other.Payload[4:])
	swapped := append(append([]byte(nil), otherRaw[:16]...), raw[16:]...) // another app's ID, this member's MAC

	for name, payload := range map[string]string{
		"app id":      flip(0),
		"member id":   flip(20),
		"window":      flip(35),
		"mac":         flip(40),
		"app swapped": "ml1." + base64.RawURLEncoding.EncodeToString(swapped),
		"other key": func() string {
			o, _ := New(bytes.Repeat([]byte{8}, 32), clock.At(time.Unix(1_800_000_000, 0)))
			x, _ := o.Card(app, member, 1)
			return x.Payload
		}(),
	} {
		p, err := Parse(payload)
		if err != nil {
			t.Errorf("%s: parse failed: %v", name, err)
			continue
		}
		if err := c.Verify(p, 1); err == nil {
			t.Errorf("%s: tampered code verified", name)
		}
	}
	for _, bad := range []string{"", "ml1.", "ml2." + cd.Payload[4:], cd.Payload + "A", cd.Payload[:len(cd.Payload)-2], "ml1.!!!", strings.Repeat("A", 200)} {
		if _, err := Parse(bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("Parse(%q) = %v, want ErrMalformed", bad, err)
		}
	}
}

func TestScanTokenRoundTripAndTampering(t *testing.T) {
	clk := clock.At(time.Unix(1_800_000_000, 0))
	c := codec(t, clk)
	staff, member, app, tenant := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	tok, exp, err := c.IssueScanToken(staff, member, app, tenant, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(clk.Now().Add(ScanTokenTTL)) {
		t.Errorf("expiry %s", exp)
	}
	cl, err := c.VerifyScanToken(tok)
	if err != nil || cl.StaffID != staff || cl.MemberID != member || cl.AppID != app || cl.TenantID != tenant {
		t.Fatalf("verify: %+v %v", cl, err)
	}

	enc, sig, _ := strings.Cut(strings.TrimPrefix(tok, "st1."), ".")
	body, _ := base64.RawURLEncoding.DecodeString(enc)
	forged := strings.Replace(string(body), member.String(), uuid.New().String(), 1)
	for name, bad := range map[string]string{
		"body swapped": "st1." + base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + sig,
		"no signature": "st1." + enc,
		"bad sig b64":  "st1." + enc + ".!!",
		"wrong prefix": "st2." + enc + "." + sig,
		"qr as token":  func() string { x, _ := c.Card(app, member, 1); return x.Payload }(),
	} {
		if _, err := c.VerifyScanToken(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	other, _ := New(bytes.Repeat([]byte{8}, 32), clk)
	if _, err := other.VerifyScanToken(tok); !errors.Is(err, ErrInvalid) {
		t.Errorf("token from another key: %v", err)
	}

	clk.Advance(ScanTokenTTL)
	if _, err := c.VerifyScanToken(tok); !errors.Is(err, ErrExpired) {
		t.Errorf("expired token: %v", err)
	}
}
