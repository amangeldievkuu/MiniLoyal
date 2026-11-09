package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

func testSigner(clk clock.Clock) *signer {
	return &signer{key: []byte("0123456789abcdef0123456789abcdef"), issuer: "miniloyal", clock: clk}
}

func TestIssueAndVerify(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 123_000_000, time.UTC))
	s := testSigner(clk)
	sub := uuid.New()

	raw, issued, err := s.issue(sub, typDashboard, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.verify(raw, typDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != sub || got.Typ != typDashboard {
		t.Errorf("claims = %+v", got)
	}
	if !got.IssuedAt.Equal(issued.Truncate(time.Millisecond)) {
		t.Errorf("issued at %s, want %s (millisecond precision)", got.IssuedAt, issued)
	}
}

func TestVerifyRejectsWrongKind(t *testing.T) {
	s := testSigner(clock.At(time.Now()))
	raw, _, _ := s.issue(uuid.New(), typMFA, 5*time.Minute)
	if _, err := s.verify(raw, typDashboard); err == nil {
		t.Error("an MFA token must not verify as a session")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	s := testSigner(clk)
	raw, _, _ := s.issue(uuid.New(), typDashboard, 15*time.Minute)
	clk.Advance(15*time.Minute + acceptableSkew + time.Second)
	if _, err := s.verify(raw, typDashboard); err == nil {
		t.Error("expired token verified")
	}
	clk.Advance(-2 * time.Second)
	if _, err := s.verify(raw, typDashboard); err != nil {
		t.Error("token within skew rejected")
	}
}

func TestVerifyRejectsOtherKeyAndIssuer(t *testing.T) {
	clk := clock.At(time.Now())
	a := testSigner(clk)
	b := &signer{key: []byte("fedcba9876543210fedcba9876543210"), issuer: "miniloyal", clock: clk}
	raw, _, _ := a.issue(uuid.New(), typDashboard, time.Minute)
	if _, err := b.verify(raw, typDashboard); err == nil {
		t.Error("token signed with another key verified")
	}
	c := &signer{key: a.key, issuer: "someone-else", clock: clk}
	if _, err := c.verify(raw, typDashboard); err == nil {
		t.Error("token for another issuer verified")
	}
	if _, err := a.verify("not.a.jwt", typDashboard); err == nil {
		t.Error("garbage verified")
	}
}
