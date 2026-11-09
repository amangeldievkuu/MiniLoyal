package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/auth/authtest"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

type authEnv struct {
	t   *testing.T
	srv *httptest.Server
	// clk drives the auth service; tests advance it between TOTP codes.
	clk *clock.Fixed
}

func newAuthEnv(t *testing.T, cfg auth.Config) *authEnv {
	t.Helper()
	d := testutil.NewDB(t)
	cfg.SigningKey = []byte("integration-test-signing-key-32b!")
	if cfg.IPPerMinute == 0 {
		cfg.IPPerMinute = 10000
	}
	if cfg.EmailPerMinute == 0 {
		cfg.EmailPerMinute = 10000
	}
	if cfg.UserPerMinute == 0 {
		cfg.UserPerMinute = 10000
	}
	clk := clock.At(time.Now())
	svc, err := auth.New(auth.Deps{Pool: d.App, Clock: clk, Encryptor: authtest.PlainEncryptor{}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Deps{
		Config:  &config.Config{Env: config.Development, PublicBaseURL: "http://localhost:8080"},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:      d.App,
		Version: "test",
		Auth:    svc,
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &authEnv{t: t, srv: srv, clk: clk}
}

type resp struct {
	status  int
	body    []byte
	headers http.Header
	cookies []*http.Cookie
}

func (e *authEnv) do(method, path string, body any, headers map[string]string) resp {
	e.t.Helper()
	// Time passes between requests, as it does for real clients; tokens
	// issued before an invalidation must then be older than it.
	e.clk.Advance(time.Millisecond)
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, buf)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: b, headers: res.Header, cookies: res.Cookies()}
}

func (r resp) into(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r resp) refreshCookie(t *testing.T) *http.Cookie {
	t.Helper()
	for _, c := range r.cookies {
		if c.Name == refreshCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", refreshCookieName, r.headers["Set-Cookie"])
	return nil
}

func cookieHeader(c *http.Cookie) map[string]string {
	return map[string]string{"Cookie": refreshCookieName + "=" + c.Value}
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func (e *authEnv) register(email string) (api.Session, *http.Cookie) {
	e.t.Helper()
	r := e.do(http.MethodPost, "/v1/dashboard/auth/register", map[string]any{
		"email": email, "password": "correct horse battery", "tenant_name": "Alpha Cafe", "tenant_type": "brand",
	}, nil)
	if r.status != http.StatusCreated {
		e.t.Fatalf("register: %d %s", r.status, r.body)
	}
	var sess api.Session
	r.into(e.t, &sess)
	return sess, r.refreshCookie(e.t)
}

func TestRegisterSetsSessionAndCookie(t *testing.T) {
	e := newAuthEnv(t, auth.Config{})
	sess, c := e.register("owner@example.com")

	if sess.AccessToken == "" || sess.TokenType != api.Bearer || sess.ExpiresIn != 900 || string(sess.User.Email) != "owner@example.com" {
		t.Errorf("unexpected session: %+v", sess)
	}
	if !c.HttpOnly || c.Path != refreshCookiePath || c.SameSite != http.SameSiteLaxMode || c.MaxAge <= 0 || c.Secure {
		t.Errorf("unexpected cookie attributes: %+v", c)
	}
	if strings.Contains(string(e.do(http.MethodPost, "/v1/dashboard/auth/register", nil, nil).body), c.Value) {
		t.Error("refresh token leaked into a body")
	}

	dup := e.do(http.MethodPost, "/v1/dashboard/auth/register", map[string]any{
		"email": "owner@example.com", "password": "correct horse battery", "tenant_name": "Beta", "tenant_type": "agency",
	}, nil)
	if dup.status != http.StatusConflict {
		t.Errorf("duplicate email: %d %s", dup.status, dup.body)
	}
	bad := e.do(http.MethodPost, "/v1/dashboard/auth/register", map[string]any{
		"email": "x", "password": "short", "tenant_name": "", "tenant_type": "club",
	}, nil)
	if bad.status != http.StatusBadRequest {
		t.Errorf("invalid register: %d %s", bad.status, bad.body)
	}
}

func TestLoginMeAndRefresh(t *testing.T) {
	e := newAuthEnv(t, auth.Config{})
	e.register("owner@example.com")

	wrong := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "nope nope nope"}, nil)
	if wrong.status != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d %s", wrong.status, wrong.body)
	}

	login := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "correct horse battery"}, nil)
	if login.status != http.StatusOK {
		t.Fatalf("login: %d %s", login.status, login.body)
	}
	var result api.LoginResult
	login.into(t, &result)
	if result.Session == nil || result.Mfa != nil {
		t.Fatalf("login result: %+v", result)
	}
	c0 := login.refreshCookie(t)

	if r := e.do(http.MethodGet, "/v1/dashboard/me", nil, nil); r.status != http.StatusUnauthorized {
		t.Errorf("me without token: %d", r.status)
	}
	me := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(result.Session.AccessToken))
	if me.status != http.StatusOK {
		t.Fatalf("me: %d %s", me.status, me.body)
	}
	var profile api.Me
	me.into(t, &profile)
	if len(profile.Memberships) != 1 || profile.Memberships[0].Role != api.RoleOwner || profile.Memberships[0].Tenant.Name != "Alpha Cafe" {
		t.Errorf("me = %+v", profile)
	}

	noCookie := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, nil)
	if noCookie.status != http.StatusUnauthorized {
		t.Errorf("refresh without cookie: %d", noCookie.status)
	}
	refreshed := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c0))
	if refreshed.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", refreshed.status, refreshed.body)
	}
	c1 := refreshed.refreshCookie(t)
	if c1.Value == c0.Value {
		t.Error("refresh did not rotate the cookie")
	}
	var sess api.Session
	refreshed.into(t, &sess)
	if r := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(sess.AccessToken)); r.status != http.StatusOK {
		t.Errorf("me with refreshed token: %d", r.status)
	}

	// Lost response: the old cookie works once more while its replacement is unused.
	again := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c0))
	if again.status != http.StatusOK {
		t.Errorf("lost-response retry: %d %s", again.status, again.body)
	}
	// The replacement gets used, so the old cookie is now a replay.
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c1)); r.status != http.StatusOK {
		t.Fatalf("refresh with c1: %d", r.status)
	}
	replay := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c0))
	if replay.status != http.StatusUnauthorized {
		t.Errorf("replay: %d %s", replay.status, replay.body)
	}
	if cleared := replay.refreshCookie(t); cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("replay should clear the cookie: %+v", cleared)
	}
}

func TestLogoutAndLogoutAll(t *testing.T) {
	e := newAuthEnv(t, auth.Config{})
	sess, c := e.register("owner@example.com")

	out := e.do(http.MethodPost, "/v1/dashboard/auth/logout", nil, cookieHeader(c))
	if out.status != http.StatusNoContent {
		t.Fatalf("logout: %d", out.status)
	}
	if cleared := out.refreshCookie(t); cleared.MaxAge >= 0 {
		t.Errorf("logout should clear the cookie: %+v", cleared)
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c)); r.status != http.StatusUnauthorized {
		t.Errorf("refresh after logout: %d", r.status)
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/logout", nil, nil); r.status != http.StatusNoContent {
		t.Errorf("logout without cookie: %d", r.status)
	}

	// Access tokens stay valid until logout-all.
	if r := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(sess.AccessToken)); r.status != http.StatusOK {
		t.Fatalf("me after logout: %d", r.status)
	}
	time.Sleep(5 * time.Millisecond) // logout-all must be strictly after the token's issue time
	if r := e.do(http.MethodPost, "/v1/dashboard/me/logout-all", nil, bearer(sess.AccessToken)); r.status != http.StatusNoContent {
		t.Fatalf("logout-all: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(sess.AccessToken)); r.status != http.StatusUnauthorized {
		t.Errorf("me after logout-all: %d", r.status)
	}
}

func TestChangePasswordInvalidatesOldSessions(t *testing.T) {
	e := newAuthEnv(t, auth.Config{})
	old, oldCookie := e.register("owner@example.com")
	time.Sleep(5 * time.Millisecond)

	forbidden := e.do(http.MethodPost, "/v1/dashboard/me/password", map[string]any{"current_password": "wrong wrong wrong", "new_password": "brand new password"}, bearer(old.AccessToken))
	if forbidden.status != http.StatusForbidden {
		t.Errorf("wrong current password: %d %s", forbidden.status, forbidden.body)
	}
	changed := e.do(http.MethodPost, "/v1/dashboard/me/password", map[string]any{"current_password": "correct horse battery", "new_password": "brand new password"}, bearer(old.AccessToken))
	if changed.status != http.StatusOK {
		t.Fatalf("change password: %d %s", changed.status, changed.body)
	}
	var fresh api.Session
	changed.into(t, &fresh)
	changed.refreshCookie(t)

	if r := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(old.AccessToken)); r.status != http.StatusUnauthorized {
		t.Errorf("old access token: %d", r.status)
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(fresh.AccessToken)); r.status != http.StatusOK {
		t.Errorf("new access token: %d", r.status)
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(oldCookie)); r.status != http.StatusUnauthorized {
		t.Errorf("old refresh cookie: %d", r.status)
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "brand new password"}, nil); r.status != http.StatusOK {
		t.Errorf("login with new password: %d", r.status)
	}
}

func TestTOTPOverHTTP(t *testing.T) {
	e := newAuthEnv(t, auth.Config{})
	sess, _ := e.register("owner@example.com")
	h := bearer(sess.AccessToken)

	enroll := e.do(http.MethodPost, "/v1/dashboard/me/totp/enroll", nil, h)
	if enroll.status != http.StatusOK {
		t.Fatalf("enroll: %d %s", enroll.status, enroll.body)
	}
	var enrollment api.TotpEnrollment
	enroll.into(t, &enrollment)
	if !strings.HasPrefix(enrollment.OtpauthUrl, "otpauth://totp/MiniLoyal:") {
		t.Errorf("otpauth url = %s", enrollment.OtpauthUrl)
	}
	// Each code is for a fresh 30-second step: an accepted code cannot be
	// reused, so the clock moves on before every one.
	code := func() string {
		e.clk.Advance(31 * time.Second)
		c, err := totp.GenerateCode(enrollment.Secret, e.clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/me/totp/confirm", map[string]any{"code": "000000"}, h); r.status != http.StatusBadRequest {
		t.Errorf("wrong confirm code: %d %s", r.status, r.body)
	}
	confirm := e.do(http.MethodPost, "/v1/dashboard/me/totp/confirm", map[string]any{"code": code()}, h)
	if confirm.status != http.StatusOK {
		t.Fatalf("confirm: %d %s", confirm.status, confirm.body)
	}
	var confirmed api.TotpConfirmed
	confirm.into(t, &confirmed)
	if len(confirmed.RecoveryCodes) != 8 {
		t.Fatalf("recovery codes: %v", confirmed.RecoveryCodes)
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/me/totp/enroll", nil, h); r.status != http.StatusConflict {
		t.Errorf("enroll when enabled: %d", r.status)
	}

	login := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "correct horse battery"}, nil)
	var result api.LoginResult
	login.into(t, &result)
	if result.Mfa == nil || result.Session != nil {
		t.Fatalf("login with totp enabled: %+v (%s)", result, login.body)
	}
	if len(login.cookies) != 0 {
		t.Error("an MFA challenge must not set the refresh cookie")
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/mfa", map[string]any{"mfa_token": result.Mfa.MfaToken, "code": "000000"}, nil); r.status != http.StatusUnauthorized {
		t.Errorf("wrong mfa code: %d %s", r.status, r.body)
	}
	mfa := e.do(http.MethodPost, "/v1/dashboard/auth/mfa", map[string]any{"mfa_token": result.Mfa.MfaToken, "code": code()}, nil)
	if mfa.status != http.StatusOK {
		t.Fatalf("mfa: %d %s", mfa.status, mfa.body)
	}
	mfa.refreshCookie(t)
	var mfaSess api.Session
	mfa.into(t, &mfaSess)
	if !mfaSess.User.TotpEnabled {
		t.Error("session user should report totp_enabled")
	}

	login2 := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "correct horse battery"}, nil)
	login2.into(t, &result)
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/mfa", map[string]any{"mfa_token": result.Mfa.MfaToken, "code": strings.ToLower(confirmed.RecoveryCodes[0])}, nil); r.status != http.StatusOK {
		t.Errorf("recovery code: %d %s", r.status, r.body)
	}
	login3 := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "correct horse battery"}, nil)
	login3.into(t, &result)
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/mfa", map[string]any{"mfa_token": result.Mfa.MfaToken, "code": confirmed.RecoveryCodes[0]}, nil); r.status != http.StatusUnauthorized {
		t.Errorf("recovery code reuse: %d", r.status)
	}

	if r := e.do(http.MethodDelete, "/v1/dashboard/me/totp", map[string]any{"password": "wrong wrong wrong", "code": code()}, h); r.status != http.StatusForbidden {
		t.Errorf("disable with wrong password: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodDelete, "/v1/dashboard/me/totp", map[string]any{"password": "correct horse battery", "code": confirmed.RecoveryCodes[1]}, h); r.status != http.StatusNoContent {
		t.Errorf("disable: %d %s", r.status, r.body)
	}
	login4 := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "correct horse battery"}, nil)
	login4.into(t, &result)
	if result.Session == nil {
		t.Errorf("login after disable should be direct: %s", login4.body)
	}
}

func TestAuthRateLimitAnswers429(t *testing.T) {
	e := newAuthEnv(t, auth.Config{IPPerMinute: 2, EmailPerMinute: 1000})
	body := map[string]any{"email": "nobody@example.com", "password": "whatever password"}
	for i := 0; i < 2; i++ {
		if r := e.do(http.MethodPost, "/v1/dashboard/auth/login", body, nil); r.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, r.status)
		}
	}
	r := e.do(http.MethodPost, "/v1/dashboard/auth/login", body, nil)
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("third attempt: %d %s", r.status, r.body)
	}
	if r.headers.Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
	var apiErr api.Error
	r.into(t, &apiErr)
	if apiErr.Code != "rate_limited" {
		t.Errorf("code = %s", apiErr.Code)
	}
}

func TestAuthEndpointsWithoutServiceAre503(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/auth/login", strings.NewReader(`{"email":"a@b.co","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rec.Code)
	}
}
