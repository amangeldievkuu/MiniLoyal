package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// enrollTOTP enables TOTP for the session's user and returns the secret. It
// advances the clock past the confirmation code's step.
func enrollTOTP(t *testing.T, svc *auth.Service, clk *clock.Fixed, sess *auth.Session) string {
	t.Helper()
	ctx := context.Background()
	secret, _, err := svc.EnrollTOTP(ctx, sess.User.ID)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	code, err := totp.GenerateCode(secret, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmTOTP(ctx, sess.User.ID, code); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	clk.Advance(31 * time.Second)
	return secret
}

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func challenge(t *testing.T, svc *auth.Service, email string) string {
	t.Helper()
	res, err := svc.Login(context.Background(), email, "correct horse battery", client)
	if err != nil || res.MFA == nil {
		t.Fatalf("login: %v %+v", err, res)
	}
	return res.MFA.Token
}

func TestMFAChallengeIsCappedAndSingleUse(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()
	sess := register(t, svc, "cap@example.com", "correct horse battery")
	secret := enrollTOTP(t, svc, clk, sess)

	token := challenge(t, svc, "cap@example.com")
	for i := 1; i <= 5; i++ {
		if _, err := svc.CompleteMFA(ctx, token, "000000", client); status(t, err) != 401 {
			t.Fatalf("attempt %d: got %v, want 401", i, err)
		}
	}
	// The sixth attempt is refused even with the right code: the challenge is dead.
	if _, err := svc.CompleteMFA(ctx, token, codeAt(t, secret, clk.Now()), client); status(t, err) != 401 {
		t.Fatalf("exhausted challenge accepted a code: %v", err)
	}

	// A fresh challenge works once, then never again.
	token = challenge(t, svc, "cap@example.com")
	if _, err := svc.CompleteMFA(ctx, token, codeAt(t, secret, clk.Now()), client); err != nil {
		t.Fatalf("fresh challenge: %v", err)
	}
	clk.Advance(31 * time.Second)
	if _, err := svc.CompleteMFA(ctx, token, codeAt(t, secret, clk.Now()), client); status(t, err) != 401 {
		t.Fatalf("used challenge accepted again: %v", err)
	}
}

func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()
	sess := register(t, svc, "replay@example.com", "correct horse battery")
	secret := enrollTOTP(t, svc, clk, sess)

	code := codeAt(t, secret, clk.Now())
	if _, err := svc.CompleteMFA(ctx, challenge(t, svc, "replay@example.com"), code, client); err != nil {
		t.Fatalf("first use: %v", err)
	}
	// Same code, same window, new challenge: refused as a replay.
	if _, err := svc.CompleteMFA(ctx, challenge(t, svc, "replay@example.com"), code, client); status(t, err) != 401 {
		t.Fatalf("replayed code accepted: %v", err)
	}
	// A code for an older step is refused too.
	if _, err := svc.CompleteMFA(ctx, challenge(t, svc, "replay@example.com"), codeAt(t, secret, clk.Now().Add(-30*time.Second)), client); status(t, err) != 401 {
		t.Fatalf("older step accepted: %v", err)
	}
	clk.Advance(31 * time.Second)
	if _, err := svc.CompleteMFA(ctx, challenge(t, svc, "replay@example.com"), codeAt(t, secret, clk.Now()), client); err != nil {
		t.Fatalf("next step: %v", err)
	}
}

func TestRefreshReplayOutsideWindowRevokesFamily(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()

	s0 := register(t, svc, "window@example.com", "correct horse battery")
	s1, err := svc.Refresh(ctx, s0.RefreshToken, client)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	if _, err := svc.Refresh(ctx, s0.RefreshToken, client); status(t, err) != 401 {
		t.Fatalf("late replay: got %v, want 401", err)
	}
	if _, err := svc.Refresh(ctx, s1.RefreshToken, client); status(t, err) != 401 {
		t.Fatalf("family should be revoked after a late replay: %v", err)
	}
}

func TestForkedRefreshFamilyIsRevoked(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()

	// The attacker holds a copy of A. Victim: A -> B. Attacker replays A inside
	// the allowance and gets C, then moves on to C1. When the victim rotates B,
	// B's sibling C has been used: two branches are live, the family dies.
	s0 := register(t, svc, "fork@example.com", "correct horse battery")
	victimB, err := svc.Refresh(ctx, s0.RefreshToken, client)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	attackerC, err := svc.Refresh(ctx, s0.RefreshToken, auth.Client{IP: "198.51.100.9"})
	if err != nil {
		t.Fatalf("allowance within the window: %v", err)
	}
	attackerC1, err := svc.Refresh(ctx, attackerC.RefreshToken, auth.Client{IP: "198.51.100.9"})
	if err != nil {
		t.Fatalf("attacker's first rotation is indistinguishable: %v", err)
	}
	if _, err := svc.Refresh(ctx, victimB.RefreshToken, client); status(t, err) != 401 {
		t.Fatalf("victim's rotation should detect the fork: %v", err)
	}
	if _, err := svc.Refresh(ctx, attackerC1.RefreshToken, auth.Client{IP: "198.51.100.9"}); status(t, err) != 401 {
		t.Fatalf("attacker's branch should be dead: %v", err)
	}
}

func TestTwoTabsRefreshingTogetherStaySignedIn(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()

	// Two tabs refresh with the same cookie A; the browser keeps whichever
	// response landed last (C) and never uses B. That must keep working.
	s0 := register(t, svc, "tabs@example.com", "correct horse battery")
	if _, err := svc.Refresh(ctx, s0.RefreshToken, client); err != nil {
		t.Fatal(err)
	}
	c, err := svc.Refresh(ctx, s0.RefreshToken, client)
	if err != nil {
		t.Fatalf("second tab: %v", err)
	}
	cur := c.RefreshToken
	for i := 0; i < 3; i++ {
		clk.Advance(5 * time.Minute)
		next, err := svc.Refresh(ctx, cur, client)
		if err != nil {
			t.Fatalf("rotation %d after two tabs: %v", i, err)
		}
		cur = next.RefreshToken
	}
}

func TestChangePasswordIsRateLimitedPerUser(t *testing.T) {
	d := testutil.NewDB(t)
	svc := newService(t, d, clock.Real{}, auth.Config{UserPerMinute: 2})
	ctx := context.Background()
	sess := register(t, svc, "limit@example.com", "correct horse battery")

	for i := 0; i < 2; i++ {
		if _, err := svc.ChangePassword(ctx, sess.User.ID, "wrong password here", "another long password", client); status(t, err) != 403 {
			t.Fatalf("attempt %d: got %v, want 403", i, err)
		}
	}
	if _, err := svc.ChangePassword(ctx, sess.User.ID, "correct horse battery", "another long password", client); status(t, err) != 429 {
		t.Fatalf("third attempt: got %v, want 429", err)
	}
}

func TestPasswordChangeInTheSameMillisecondKeepsTheNewSession(t *testing.T) {
	d := testutil.NewDB(t)
	// A clock with sub-millisecond nanoseconds, frozen so the invalidation
	// and the new token share the same instant.
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 500700, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()
	sess := register(t, svc, "ms@example.com", "correct horse battery")
	// 1.5 ms later: the invalidation instant truncates to .002 while the real
	// clock reads .0020007. Before the fix the new token (iat_ms .002) was
	// older than sessions_valid_from (.0020007) and was rejected.
	clk.Advance(1500 * time.Microsecond)

	next, err := svc.ChangePassword(ctx, sess.User.ID, "correct horse battery", "a brand new password", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateDashboard(ctx, next.AccessToken); err != nil {
		t.Fatalf("the session issued by ChangePassword must be valid: %v", err)
	}
	if _, err := svc.AuthenticateDashboard(ctx, sess.AccessToken); status(t, err) != 401 {
		t.Fatalf("the old session must be invalid: %v", err)
	}
}

func TestVerifyPasswordRefusesAbsurdParameters(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := auth.VerifyPassword(hash, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("sane hash: %v %v", ok, err)
	}
	for _, bad := range []string{
		"$argon2id$v=19$m=999999999,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=19456,t=0,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=19456,t=2,p=0$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
	} {
		if _, err := auth.VerifyPassword(bad, "x"); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
