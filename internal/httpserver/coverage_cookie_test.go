package httpserver

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

const thirtyDays = 30 * 24 * 60 * 60

// assertSessionCookie checks every attribute of a freshly set refresh cookie.
func assertSessionCookie(t *testing.T, c *http.Cookie, secure bool, maxAge int) {
	t.Helper()
	if c.Value == "" {
		t.Error("cookie has no value")
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/v1/dashboard/auth" {
		t.Errorf("Path = %q, want /v1/dashboard/auth", c.Path)
	}
	if c.MaxAge != maxAge {
		t.Errorf("Max-Age = %d, want %d", c.MaxAge, maxAge)
	}
	if c.Secure != secure {
		t.Errorf("Secure = %v, want %v", c.Secure, secure)
	}
	if c.Domain != "" {
		t.Errorf("Domain = %q, want host-only", c.Domain)
	}
}

// assertClearedCookie checks a cookie that tells the browser to forget the
// refresh token.
func assertClearedCookie(t *testing.T, c *http.Cookie) {
	t.Helper()
	if c.Value != "" {
		t.Error("cleared cookie still carries a value")
	}
	if c.MaxAge >= 0 {
		t.Errorf("Max-Age = %d, want negative", c.MaxAge)
	}
	if c.Path != "/v1/dashboard/auth" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cleared cookie must keep Path, HttpOnly and SameSite so the browser matches it: %+v", c)
	}
}

// Invariant: every response that starts or rotates a session sets the
// refresh cookie with HttpOnly, SameSite=Lax, Path=/v1/dashboard/auth and a
// 30-day Max-Age, and never puts the refresh token in the body.
func TestRefreshCookieAttributesOnEverySessionResponse(t *testing.T) {
	e := newFullEnv(t, fullOpts{})
	reg := e.do(http.MethodPost, "/v1/dashboard/auth/register", map[string]any{
		"email": "owner@example.com", "password": "correct horse battery", "tenant_name": "Alpha", "tenant_type": "brand",
	}, nil)
	if reg.status != http.StatusCreated {
		t.Fatalf("register: %d %s", reg.status, reg.body)
	}
	responses := map[string]resp{"register": reg}

	_, login := e.login("owner@example.com", "correct horse battery", nil)
	responses["login"] = login

	refreshed := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(login.refreshCookie(t)))
	if refreshed.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", refreshed.status, refreshed.body)
	}
	responses["refresh"] = refreshed

	var sess api.Session
	refreshed.into(t, &sess)
	changed := e.do(http.MethodPost, "/v1/dashboard/me/password", map[string]any{"current_password": "correct horse battery", "new_password": "brand new password!"}, bearer(sess.AccessToken))
	if changed.status != http.StatusOK {
		t.Fatalf("change password: %d %s", changed.status, changed.body)
	}
	responses["password change"] = changed

	changed.into(t, &sess)
	secret, _ := e.enrollTOTP(sess.AccessToken, e.now())
	challenge, _ := e.login("owner@example.com", "brand new password!", nil)
	mfa := e.do(http.MethodPost, "/v1/dashboard/auth/mfa", map[string]any{"mfa_token": challenge.Mfa.MfaToken, "code": totpCode(t, secret, e.now())}, nil)
	if mfa.status != http.StatusOK {
		t.Fatalf("mfa: %d %s", mfa.status, mfa.body)
	}
	responses["mfa"] = mfa

	for name, r := range responses {
		t.Run(name, func(t *testing.T) {
			if n := len(r.cookies); n != 1 {
				t.Fatalf("%d cookies set, want exactly the refresh cookie: %v", n, r.headers["Set-Cookie"])
			}
			c := r.refreshCookie(t)
			assertSessionCookie(t, c, false, thirtyDays)
			if bytes.Contains(r.body, []byte(c.Value)) {
				t.Error("refresh token appears in the response body")
			}
		})
	}
	// The cookie value rotates on every refresh.
	if login.refreshCookie(t).Value == refreshed.refreshCookie(t).Value {
		t.Error("refresh did not rotate the cookie")
	}
}

// Invariant: the Secure flag follows the real scheme. X-Forwarded-Proto is
// trusted only when TrustProxy is on; a plain request never gets Secure (the
// browser would drop the cookie on http://localhost).
func TestRefreshCookieSecureFlag(t *testing.T) {
	register := func(e *fullEnv, email string, headers map[string]string) *http.Cookie {
		t.Helper()
		r := e.do(http.MethodPost, "/v1/dashboard/auth/register", map[string]any{
			"email": email, "password": "correct horse battery", "tenant_name": "Alpha", "tenant_type": "brand",
		}, headers)
		if r.status != http.StatusCreated {
			t.Fatalf("register: %d %s", r.status, r.body)
		}
		return r.refreshCookie(t)
	}

	t.Run("proxy not trusted", func(t *testing.T) {
		e := newFullEnv(t, fullOpts{cfg: config.Config{TrustProxy: false}})
		if c := register(e, "a@example.com", nil); c.Secure {
			t.Error("plain request got Secure")
		}
		if c := register(e, "b@example.com", map[string]string{"X-Forwarded-Proto": "https"}); c.Secure {
			t.Error("X-Forwarded-Proto was honoured without TrustProxy")
		}
	})
	t.Run("proxy trusted", func(t *testing.T) {
		e := newFullEnv(t, fullOpts{cfg: config.Config{TrustProxy: true}})
		if c := register(e, "a@example.com", map[string]string{"X-Forwarded-Proto": "https"}); !c.Secure {
			t.Error("https behind the proxy did not get Secure")
		}
		if c := register(e, "b@example.com", map[string]string{"X-Forwarded-Proto": "HTTPS "}); !c.Secure {
			t.Error("scheme comparison must be case-insensitive and trimmed")
		}
		if c := register(e, "c@example.com", map[string]string{"X-Forwarded-Proto": "http"}); c.Secure {
			t.Error("http behind the proxy got Secure")
		}
		if c := register(e, "d@example.com", nil); c.Secure {
			t.Error("request without X-Forwarded-Proto got Secure")
		}
		// Cleared cookies follow the same rule, or the browser ignores them.
		out := e.do(http.MethodPost, "/v1/dashboard/auth/logout", nil, map[string]string{"X-Forwarded-Proto": "https"})
		if c := out.refreshCookie(t); !c.Secure {
			t.Error("cleared cookie over https is not Secure")
		}
	})
	t.Run("public base URL is https", func(t *testing.T) {
		e := newFullEnv(t, fullOpts{cfg: config.Config{PublicBaseURL: "https://api.example.com"}})
		if c := register(e, "a@example.com", nil); !c.Secure {
			t.Error("an https deployment must always mark the cookie Secure")
		}
	})
}

// Invariant: the cookie lifetime follows the refresh token TTL.
func TestRefreshCookieMaxAgeFollowsRefreshTTL(t *testing.T) {
	e := newFullEnv(t, fullOpts{authCfg: auth.Config{RefreshTTL: 7 * 24 * time.Hour}})
	_, c := e.register("owner@example.com", "correct horse battery")
	assertSessionCookie(t, c, false, 7*24*60*60)
	if auth.DefaultRefreshTTL != 30*24*time.Hour {
		t.Errorf("DefaultRefreshTTL = %s, the documented cookie lifetime is 30 days", auth.DefaultRefreshTTL)
	}
}

// Invariant: logout, logout-all and a refused refresh clear the cookie, and
// a cleared cookie no longer refreshes.
func TestRefreshCookieIsClearedWhenTheSessionEnds(t *testing.T) {
	e := newFullEnv(t, fullOpts{})
	sess, c := e.register("owner@example.com", "correct horse battery")

	out := e.do(http.MethodPost, "/v1/dashboard/auth/logout", nil, cookieHeader(c))
	if out.status != http.StatusNoContent {
		t.Fatalf("logout: %d", out.status)
	}
	assertClearedCookie(t, out.refreshCookie(t))

	replay := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c))
	if replay.status != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: %d", replay.status)
	}
	assertClearedCookie(t, replay.refreshCookie(t))

	bogus := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, map[string]string{"Cookie": refreshCookieName + "=not-a-token"})
	if bogus.status != http.StatusUnauthorized {
		t.Fatalf("refresh with a bogus cookie: %d", bogus.status)
	}
	assertClearedCookie(t, bogus.refreshCookie(t))

	none := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, nil)
	if none.status != http.StatusUnauthorized {
		t.Fatalf("refresh without a cookie: %d", none.status)
	}

	all := e.do(http.MethodPost, "/v1/dashboard/me/logout-all", nil, bearer(sess.AccessToken))
	if all.status != http.StatusNoContent {
		t.Fatalf("logout-all: %d %s", all.status, all.body)
	}
	assertClearedCookie(t, all.refreshCookie(t))

	// An MFA challenge and an error response set no session cookie.
	wrong := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": "owner@example.com", "password": "nope nope nope"}, nil)
	if wrong.status != http.StatusUnauthorized || len(wrong.cookies) != 0 {
		t.Errorf("failed login: %d, cookies %v", wrong.status, wrong.headers["Set-Cookie"])
	}
}
