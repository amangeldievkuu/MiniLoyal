package httpserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

// safeBuffer is a bytes.Buffer that background goroutines (API key touches,
// refresh token sweeps) may write to while the test reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Invariant: whatever the handlers and services log at debug level, no
// credential ever appears: passwords, access and MFA tokens, refresh
// tokens, API keys, recovery codes, TOTP secrets, channel secrets.
func TestLogsNeverContainCredentials(t *testing.T) {
	var logs safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newFullEnv(t, fullOpts{log: logger})

	const (
		password      = "correct horse battery"
		wrongPassword = "wrong horse battery staple"
		newPassword   = "brand new password!"
		oaSecret      = "oa-channel-secret-0123456789"
		channelSecret = "liff-channel-secret-abcdef"
	)
	var secrets []string
	add := func(label string, values ...string) {
		for _, v := range values {
			if v == "" {
				t.Fatalf("empty secret for %s", label)
			}
			secrets = append(secrets, v)
		}
	}
	add("passwords", password, wrongPassword, newPassword)

	// Register and sign in, including a failed attempt.
	sess, c0 := e.register("owner@example.com", password)
	add("register access token", sess.AccessToken)
	add("register refresh token", c0.Value)
	if _, r := e.login("owner@example.com", wrongPassword, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", r.status)
	}
	res, r := e.login("owner@example.com", password, nil)
	if r.status != http.StatusOK || res.Session == nil {
		t.Fatalf("login: %d %s", r.status, r.body)
	}
	add("login access token", res.Session.AccessToken)
	add("login refresh token", r.refreshCookie(t).Value)

	// Refresh, then replay the first cookie after its chain moved on so the
	// replay warning is logged too.
	c1 := r.refreshCookie(t)
	r1 := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c1))
	if r1.status != http.StatusOK {
		t.Fatalf("refresh: %d %s", r1.status, r1.body)
	}
	c2 := r1.refreshCookie(t)
	add("rotated refresh token", c2.Value)
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c2)); r.status != http.StatusOK {
		t.Fatalf("second refresh: %d", r.status)
	} else {
		add("second rotated refresh token", r.refreshCookie(t).Value)
	}
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/refresh", nil, cookieHeader(c1)); r.status != http.StatusUnauthorized {
		t.Fatalf("replay should be refused: %d", r.status)
	}

	// TOTP: enrollment secret, recovery codes, a failed and a successful MFA.
	access := sess.AccessToken
	secret, codes := e.enrollTOTP(access, e.now())
	add("totp secret", secret)
	for _, code := range codes {
		add("recovery code", code, strings.ReplaceAll(code, "-", ""), strings.ToLower(code))
	}
	challenge, r := e.login("owner@example.com", password, nil)
	if challenge.Mfa == nil {
		t.Fatalf("expected an MFA challenge: %s", r.body)
	}
	add("mfa token", challenge.Mfa.MfaToken)
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/mfa", map[string]any{"mfa_token": challenge.Mfa.MfaToken, "code": "000000"}, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("wrong mfa code: %d", r.status)
	}
	totp := totpCode(t, secret, e.now())
	add("totp code", totp)
	mfa := e.do(http.MethodPost, "/v1/dashboard/auth/mfa", map[string]any{"mfa_token": challenge.Mfa.MfaToken, "code": codes[0]}, nil)
	if mfa.status != http.StatusOK {
		t.Fatalf("mfa with recovery code: %d %s", mfa.status, mfa.body)
	}
	var mfaSess api.Session
	mfa.into(t, &mfaSess)
	add("mfa access token", mfaSess.AccessToken)
	add("mfa refresh token", mfa.refreshCookie(t).Value)

	// Tenant resources with secrets: an API key, the OA secret, a channel
	// secret. Use the key once so the touch path runs.
	app := e.createApp(access, "Cafe")
	key := e.createAPIKey(access, app)
	add("api key", key.Key)
	if r := e.do(http.MethodGet, "/v1/app", nil, map[string]string{"X-API-Key": key.Key}); r.status != http.StatusOK {
		t.Fatalf("/v1/app: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/v1/app", nil, map[string]string{"X-API-Key": "ml_" + strings.Repeat("z", 40)}); r.status != http.StatusUnauthorized {
		t.Fatalf("bad key: %d", r.status)
	}
	h := bearer(access)
	h["Idempotency-Key"] = uuid.NewString()
	if r := e.do(http.MethodPatch, "/v1/dashboard/apps/"+app.Id.String(), map[string]any{"oa_channel_id": "1654000000", "oa_channel_secret": oaSecret}, h); r.status != http.StatusOK {
		t.Fatalf("oa secret: %d %s", r.status, r.body)
	}
	h["Idempotency-Key"] = uuid.NewString()
	if r := e.do(http.MethodPut, "/v1/dashboard/apps/"+app.Id.String()+"/channels/developing", map[string]any{"channel_id": "2007000001", "liff_id": "2007000001-dev", "channel_secret": channelSecret}, h); r.status != http.StatusOK {
		t.Fatalf("channel secret: %d %s", r.status, r.body)
	}
	add("oa secret", oaSecret)
	add("channel secret", channelSecret)
	// A replayed idempotency key decodes the stored (encrypted) API key.
	h["Idempotency-Key"] = uuid.NewString()
	keyBody := map[string]any{"name": "again", "scopes": []string{"members:read"}}
	first := e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/api-keys", keyBody, h)
	again := e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/api-keys", keyBody, h)
	if first.status != http.StatusCreated || again.status != http.StatusCreated {
		t.Fatalf("api key replay: %d %d", first.status, again.status)
	}
	var k2 api.ApiKeyCreated
	again.into(t, &k2)
	add("replayed api key", k2.Key)
	// A mismatch, a missing key and a 404 exercise the warning paths.
	if r := e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/api-keys", map[string]any{"name": "other", "scopes": []string{"members:read"}}, h); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: %d", r.status)
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/apps/"+uuid.NewString(), nil, bearer(access)); r.status != http.StatusNotFound {
		t.Fatalf("unknown app: %d", r.status)
	}

	// Password change and sign out everywhere.
	changed := e.do(http.MethodPost, "/v1/dashboard/me/password", map[string]any{"current_password": password, "new_password": newPassword}, bearer(mfaSess.AccessToken))
	if changed.status != http.StatusOK {
		t.Fatalf("change password: %d %s", changed.status, changed.body)
	}
	var fresh api.Session
	changed.into(t, &fresh)
	add("post-change access token", fresh.AccessToken)
	add("post-change refresh token", changed.refreshCookie(t).Value)
	if r := e.do(http.MethodPost, "/v1/dashboard/me/logout-all", nil, bearer(fresh.AccessToken)); r.status != http.StatusNoContent {
		t.Fatalf("logout-all: %d", r.status)
	}

	out := logs.String()
	if !strings.Contains(out, `"msg":"request"`) {
		t.Fatalf("log capture is not wired: %q", out)
	}
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("log output contains a credential (%d chars starting %q)", len(s), s[:min(4, len(s))])
		}
	}
	for _, marker := range []string{"$argon2id$", "Bearer ", "ml_", "ml_refresh=", "Authorization", "X-API-Key", "\"password\"", "\"secret\"", "\"key\""} {
		if strings.Contains(out, marker) {
			t.Errorf("log output contains %q, a credential marker", marker)
		}
	}
	if strings.Contains(out, "level\":\"ERROR") {
		t.Errorf("unexpected error-level logs:\n%s", out)
	}
}
