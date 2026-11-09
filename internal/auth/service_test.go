package auth_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/auth/authtest"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

var signingKey = []byte("integration-test-signing-key-32b!")

func newService(t *testing.T, d *testutil.DB, clk clock.Clock, cfg auth.Config) *auth.Service {
	t.Helper()
	cfg.SigningKey = signingKey
	if cfg.IPPerMinute == 0 {
		cfg.IPPerMinute = 10000
	}
	if cfg.EmailPerMinute == 0 {
		cfg.EmailPerMinute = 10000
	}
	if cfg.UserPerMinute == 0 {
		cfg.UserPerMinute = 10000
	}
	svc, err := auth.New(auth.Deps{Pool: d.App, Clock: clk, Encryptor: authtest.PlainEncryptor{}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

var client = auth.Client{IP: "203.0.113.7", UserAgent: "tests/1.0"}

func register(t *testing.T, svc *auth.Service, email, password string) *auth.Session {
	t.Helper()
	sess, err := svc.Register(context.Background(), auth.RegisterInput{
		Email: email, Password: password, TenantName: "Alpha Cafe", TenantType: "brand",
	}, client)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return sess
}

func status(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 200
	}
	ae, ok := apierr.As(err)
	if !ok {
		t.Fatalf("not an API error: %v", err)
	}
	return ae.Status
}

func TestRegisterLoginAndMe(t *testing.T) {
	d := testutil.NewDB(t)
	svc := newService(t, d, clock.Real{}, auth.Config{})
	ctx := context.Background()

	sess := register(t, svc, "Owner@Example.com", "correct horse battery")
	if sess.AccessToken == "" || sess.RefreshToken == "" || sess.User.Email != "owner@example.com" {
		t.Fatalf("unexpected session: %+v", sess)
	}

	user, memberships, err := svc.Me(ctx, sess.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if user.TotpEnabled || len(memberships) != 1 || memberships[0].Role != "owner" || memberships[0].Tenant.Type != "brand" || memberships[0].Tenant.Slug != "alpha-cafe" {
		t.Errorf("me = %+v %+v", user, memberships)
	}

	if _, err := svc.Register(ctx, auth.RegisterInput{Email: "owner@example.com", Password: "another password", TenantName: "Beta", TenantType: "agency"}, client); status(t, err) != 409 {
		t.Errorf("duplicate email: got %v, want 409", err)
	}
	_, err = svc.Register(ctx, auth.RegisterInput{Email: "not-an-email", Password: "short", TenantName: "", TenantType: "club"}, client)
	ae, _ := apierr.As(err)
	if ae == nil || ae.Status != 400 || ae.Details["email"] == nil || ae.Details["password"] == nil || ae.Details["tenant_name"] == nil || ae.Details["tenant_type"] == nil {
		t.Errorf("validation: got %v", err)
	}

	// A second tenant with the same name gets a different slug.
	sess2 := register(t, svc, "second@example.com", "correct horse battery")
	_, m2, _ := svc.Me(ctx, sess2.User.ID)
	if len(m2) != 1 || m2[0].Tenant.Slug == "alpha-cafe" {
		t.Errorf("slug collision not handled: %+v", m2)
	}

	res, err := svc.Login(ctx, "owner@example.com", "correct horse battery", client)
	if err != nil || res.Session == nil || res.MFA != nil {
		t.Fatalf("login: %v %+v", err, res)
	}
	if _, err := svc.Login(ctx, "owner@example.com", "wrong horse battery", client); status(t, err) != 401 {
		t.Errorf("wrong password: got %v", err)
	}
	if _, err := svc.Login(ctx, "nobody@example.com", "whatever password", client); status(t, err) != 401 {
		t.Errorf("unknown email: got %v", err)
	}

	p, err := svc.AuthenticateDashboard(ctx, res.Session.AccessToken)
	if err != nil || !p.IsUser() || p.UserID != sess.User.ID {
		t.Errorf("authenticate: %v %+v", err, p)
	}
}

func TestRefreshRotationStateMachine(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()

	s0 := register(t, svc, "rot@example.com", "correct horse battery")
	r0 := s0.RefreshToken

	s1, err := svc.Refresh(ctx, r0, client)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if s1.RefreshToken == r0 {
		t.Fatal("refresh token was not rotated")
	}

	// The client never received s1 (lost response) and retries with r0.
	s2, err := svc.Refresh(ctx, r0, client)
	if err != nil {
		t.Fatalf("lost-response retry: %v", err)
	}

	// The client did use s1, so s1's token is now rotated too.
	s3, err := svc.Refresh(ctx, s1.RefreshToken, client)
	if err != nil {
		t.Fatalf("refresh with s1: %v", err)
	}

	// Now r0 is a replay: one of its replacements was used. The family dies.
	if _, err := svc.Refresh(ctx, r0, client); status(t, err) != 401 {
		t.Fatalf("replay: got %v, want 401", err)
	}
	for name, tok := range map[string]string{"s2": s2.RefreshToken, "s3": s3.RefreshToken} {
		if _, err := svc.Refresh(ctx, tok, client); status(t, err) != 401 {
			t.Errorf("%s after family revocation: got %v, want 401", name, err)
		}
	}

	// A new sign-in starts a fresh family; a token past its expiry is refused.
	res, err := svc.Login(ctx, "rot@example.com", "correct horse battery", client)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(31 * 24 * time.Hour)
	if _, err := svc.Refresh(ctx, res.Session.RefreshToken, client); status(t, err) != 401 {
		t.Errorf("expired refresh: got %v", err)
	}
	if _, err := svc.Refresh(ctx, "", client); status(t, err) != 401 {
		t.Errorf("empty refresh: got %v", err)
	}
	if _, err := svc.Refresh(ctx, "nonsense", client); status(t, err) != 401 {
		t.Errorf("unknown refresh: got %v", err)
	}
}

func TestLostResponseAllowanceIsBounded(t *testing.T) {
	d := testutil.NewDB(t)
	svc := newService(t, d, clock.Real{}, auth.Config{})
	ctx := context.Background()
	s0 := register(t, svc, "bounded@example.com", "correct horse battery")

	for i := 0; i < auth.MaxChildren; i++ {
		if _, err := svc.Refresh(ctx, s0.RefreshToken, client); err != nil {
			t.Fatalf("reuse %d: %v", i, err)
		}
	}
	if _, err := svc.Refresh(ctx, s0.RefreshToken, client); status(t, err) != 401 {
		t.Errorf("reuse beyond MaxChildren: got %v, want 401", err)
	}
}

func TestPasswordChangeAndLogoutAllInvalidateSessions(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()

	old := register(t, svc, "pw@example.com", "correct horse battery")
	clk.Advance(time.Second)

	if _, err := svc.ChangePassword(ctx, old.User.ID, "wrong", "brand new password", client); status(t, err) != 403 {
		t.Errorf("wrong current password: got %v", err)
	}
	if _, err := svc.ChangePassword(ctx, old.User.ID, "correct horse battery", "short", client); status(t, err) != 400 {
		t.Errorf("weak new password: got %v", err)
	}
	fresh, err := svc.ChangePassword(ctx, old.User.ID, "correct horse battery", "brand new password", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateDashboard(ctx, old.AccessToken); status(t, err) != 401 {
		t.Errorf("old access token after password change: got %v", err)
	}
	if _, err := svc.AuthenticateDashboard(ctx, fresh.AccessToken); err != nil {
		t.Errorf("new access token rejected: %v", err)
	}
	if _, err := svc.Refresh(ctx, old.RefreshToken, client); status(t, err) != 401 {
		t.Errorf("old refresh token after password change: got %v", err)
	}
	if _, err := svc.Login(ctx, "pw@example.com", "correct horse battery", client); status(t, err) != 401 {
		t.Error("old password still works")
	}

	clk.Advance(time.Second)
	if err := svc.LogoutAll(ctx, old.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateDashboard(ctx, fresh.AccessToken); status(t, err) != 401 {
		t.Errorf("access token after logout-all: got %v", err)
	}
	if _, err := svc.Refresh(ctx, fresh.RefreshToken, client); status(t, err) != 401 {
		t.Errorf("refresh after logout-all: got %v", err)
	}

	// Logout revokes one device only.
	res, _ := svc.Login(ctx, "pw@example.com", "brand new password", client)
	if err := svc.Logout(ctx, res.Session.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, res.Session.RefreshToken, client); status(t, err) != 401 {
		t.Errorf("refresh after logout: got %v", err)
	}
	if err := svc.Logout(ctx, ""); err != nil {
		t.Errorf("logout without a cookie: %v", err)
	}
}

func TestTOTPFlow(t *testing.T) {
	d := testutil.NewDB(t)
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	svc := newService(t, d, clk, auth.Config{})
	ctx := context.Background()
	sess := register(t, svc, "totp@example.com", "correct horse battery")
	uid := sess.User.ID
	code := func(secret string) string {
		c, err := totp.GenerateCode(secret, clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	if _, err := svc.ConfirmTOTP(ctx, uid, "123456"); status(t, err) != 409 {
		t.Errorf("confirm without enrollment: got %v", err)
	}
	secret, url, err := svc.EnrollTOTP(ctx, uid)
	if err != nil || secret == "" || url == "" {
		t.Fatalf("enroll: %v %q %q", err, secret, url)
	}
	// Still a plain login until confirmed.
	if res, err := svc.Login(ctx, "totp@example.com", "correct horse battery", client); err != nil || res.MFA != nil {
		t.Fatalf("login before confirm: %v %+v", err, res)
	}
	if _, err := svc.ConfirmTOTP(ctx, uid, "000000"); status(t, err) != 400 {
		t.Errorf("wrong confirm code: got %v", err)
	}
	codes, err := svc.ConfirmTOTP(ctx, uid, code(secret))
	if err != nil || len(codes) != 8 {
		t.Fatalf("confirm: %v %v", err, codes)
	}
	if _, _, err := svc.EnrollTOTP(ctx, uid); status(t, err) != 409 {
		t.Errorf("enroll when enabled: got %v", err)
	}

	res, err := svc.Login(ctx, "totp@example.com", "correct horse battery", client)
	if err != nil || res.MFA == nil || res.Session != nil {
		t.Fatalf("login with totp: %v %+v", err, res)
	}
	if _, err := svc.CompleteMFA(ctx, res.MFA.Token, "000000", client); status(t, err) != 401 {
		t.Errorf("wrong mfa code: got %v", err)
	}
	if _, err := svc.CompleteMFA(ctx, "bogus", code(secret), client); status(t, err) != 401 {
		t.Errorf("bogus mfa token: got %v", err)
	}
	clk.Advance(31 * time.Second) // a fresh TOTP window
	mfaSess, err := svc.CompleteMFA(ctx, res.MFA.Token, code(secret), client)
	if err != nil || mfaSess.AccessToken == "" || !mfaSess.User.TotpEnabled {
		t.Fatalf("mfa with totp: %v %+v", err, mfaSess)
	}

	// A recovery code works once, however it is typed.
	res2, _ := svc.Login(ctx, "totp@example.com", "correct horse battery", client)
	lower := fmt.Sprintf(" %s ", codes[0])
	if _, err := svc.CompleteMFA(ctx, res2.MFA.Token, lower, client); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	res3, _ := svc.Login(ctx, "totp@example.com", "correct horse battery", client)
	if _, err := svc.CompleteMFA(ctx, res3.MFA.Token, codes[0], client); status(t, err) != 401 {
		t.Errorf("recovery code reuse: got %v", err)
	}

	// The challenge token expires.
	clk.Advance(6 * time.Minute)
	if _, err := svc.CompleteMFA(ctx, res3.MFA.Token, codes[1], client); status(t, err) != 401 {
		t.Errorf("expired challenge: got %v", err)
	}

	if err := svc.DisableTOTP(ctx, uid, "wrong password", code(secret)); status(t, err) != 403 {
		t.Errorf("disable with wrong password: got %v", err)
	}
	if err := svc.DisableTOTP(ctx, uid, "correct horse battery", "000000"); status(t, err) != 403 {
		t.Errorf("disable with wrong code: got %v", err)
	}
	if err := svc.DisableTOTP(ctx, uid, "correct horse battery", codes[1]); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if res, err := svc.Login(ctx, "totp@example.com", "correct horse battery", client); err != nil || res.Session == nil {
		t.Errorf("login after disable: %v %+v", err, res)
	}
	if err := svc.DisableTOTP(ctx, uid, "correct horse battery", codes[2]); status(t, err) != 400 {
		t.Errorf("disable when not enabled: got %v", err)
	}
}

func TestTOTPUnavailableWithoutEncryptor(t *testing.T) {
	d := testutil.NewDB(t)
	svc, err := auth.New(auth.Deps{Pool: d.App, Config: auth.Config{SigningKey: signingKey, IPPerMinute: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	sess := register(t, svc, "noenc@example.com", "correct horse battery")
	if _, _, err := svc.EnrollTOTP(context.Background(), sess.User.ID); status(t, err) != 503 {
		t.Errorf("enroll without encryptor: got %v, want 503", err)
	}
}

func TestSweepAllDeletesExpiredTokens(t *testing.T) {
	d := testutil.NewDB(t)
	svc := newService(t, d, clock.At(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)), auth.Config{})
	register(t, svc, "sweep@example.com", "correct horse battery")
	n, err := svc.SweepAll(context.Background())
	if err != nil || n < 1 {
		t.Errorf("sweep = %d, %v", n, err)
	}
}

func TestRateLimitByIPAndEmail(t *testing.T) {
	d := testutil.NewDB(t)
	svc := newService(t, d, clock.Real{}, auth.Config{IPPerMinute: 2, EmailPerMinute: 1000})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, _ = svc.Login(ctx, "x@example.com", "whatever password", client)
	}
	_, err := svc.Login(ctx, "x@example.com", "whatever password", client)
	ae, _ := apierr.As(err)
	if ae == nil || ae.Status != 429 || ae.Details["retry_after_seconds"] == nil {
		t.Errorf("third attempt: got %v, want 429 with retry_after_seconds", err)
	}
	// Another IP is unaffected.
	if _, err := svc.Login(ctx, "x@example.com", "whatever password", auth.Client{IP: "198.51.100.9"}); status(t, err) != 401 {
		t.Errorf("other IP: got %v", err)
	}

	svc2 := newService(t, d, clock.Real{}, auth.Config{IPPerMinute: 1000, EmailPerMinute: 1})
	_, _ = svc2.Login(ctx, "y@example.com", "whatever password", client)
	if _, err := svc2.Login(ctx, "Y@example.com", "whatever password", client); status(t, err) != 429 {
		t.Errorf("per-email limit: got %v", err)
	}
}
