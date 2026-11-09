package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/auth/authtest"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// fullSigningKey is the dashboard JWT key of fullEnv servers, so tests can
// forge tokens that are valid in every respect but one.
var fullSigningKey = []byte("integration-test-signing-key-32b!")

// lockedClock is a Fixed clock safe to read from request goroutines while
// the test advances it.
type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func newLockedClock(t time.Time) *lockedClock { return &lockedClock{t: t} }

func (c *lockedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *lockedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fullEnv wires the real auth and tenant services behind the HTTP handler,
// the way cmd/api does, with an injectable clock and logger.
type fullEnv struct {
	t       *testing.T
	db      *testutil.DB
	clk     *lockedClock
	auth    *auth.Service
	tenants *tenant.Service
	handler http.Handler
}

type fullOpts struct {
	cfg     config.Config
	authCfg auth.Config
	log     *slog.Logger
	// realClock uses the wall clock instead of a locked fixed clock; TOTP
	// tests need a real clock when they do not advance it.
	realClock bool
}

func newFullEnv(t *testing.T, o fullOpts) *fullEnv {
	t.Helper()
	d := testutil.NewDB(t)
	if o.log == nil {
		o.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.cfg.Env == "" {
		o.cfg.Env = config.Development
	}
	if o.cfg.PublicBaseURL == "" {
		o.cfg.PublicBaseURL = "http://localhost:8080"
	}
	o.authCfg.SigningKey = fullSigningKey
	if o.authCfg.IPPerMinute == 0 {
		o.authCfg.IPPerMinute = 10000
	}
	if o.authCfg.EmailPerMinute == 0 {
		o.authCfg.EmailPerMinute = 10000
	}
	e := &fullEnv{t: t, db: d}
	var clk clock.Clock
	if o.realClock {
		clk = clock.Real{}
	} else {
		e.clk = newLockedClock(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
		clk = e.clk
	}
	svc, err := auth.New(auth.Deps{Pool: d.App, Clock: clk, Log: o.log, Encryptor: authtest.PlainEncryptor{}, Config: o.authCfg})
	if err != nil {
		t.Fatal(err)
	}
	e.auth = svc
	e.tenants = tenant.New(d.App, crypto.Static(bytes.Repeat([]byte{7}, 32)), clk, o.log)
	e.handler = New(Deps{
		Config:  &o.cfg,
		Log:     o.log,
		DB:      d.App,
		Version: "test",
		Auth:    svc,
		APIKeys: e.tenants,
		Tenants: e.tenants,
	}).Handler()
	return e
}

// do serves one request in-process and returns the recorded response.
func (e *fullEnv) do(method, path string, body any, headers map[string]string) resp {
	e.t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		buf = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, buf)
	req.RemoteAddr = "203.0.113.7:51000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: b, headers: res.Header, cookies: res.Cookies()}
}

// register creates a dashboard account with its first tenant.
func (e *fullEnv) register(email, password string) (api.Session, *http.Cookie) {
	e.t.Helper()
	r := e.do(http.MethodPost, "/v1/dashboard/auth/register", map[string]any{
		"email": email, "password": password, "tenant_name": "Alpha Cafe", "tenant_type": "brand",
	}, nil)
	if r.status != http.StatusCreated {
		e.t.Fatalf("register: %d %s", r.status, r.body)
	}
	var sess api.Session
	r.into(e.t, &sess)
	return sess, r.refreshCookie(e.t)
}

// login signs in and returns the raw result.
func (e *fullEnv) login(email, password string, headers map[string]string) (api.LoginResult, resp) {
	e.t.Helper()
	r := e.do(http.MethodPost, "/v1/dashboard/auth/login", map[string]any{"email": email, "password": password}, headers)
	var out api.LoginResult
	if r.status == http.StatusOK {
		r.into(e.t, &out)
	}
	return out, r
}

// tenantOf returns the first tenant of the signed-in user.
func (e *fullEnv) tenantOf(access string) api.Tenant {
	e.t.Helper()
	r := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(access))
	if r.status != http.StatusOK {
		e.t.Fatalf("me: %d %s", r.status, r.body)
	}
	var me api.Me
	r.into(e.t, &me)
	if len(me.Memberships) == 0 {
		e.t.Fatal("no memberships")
	}
	return me.Memberships[0].Tenant
}

// createApp creates an app in the user's first tenant.
func (e *fullEnv) createApp(access string, name string) api.App {
	e.t.Helper()
	tn := e.tenantOf(access)
	h := bearer(access)
	h["Idempotency-Key"] = uuid.NewString()
	r := e.do(http.MethodPost, "/v1/dashboard/tenants/"+tn.Id.String()+"/apps", map[string]any{"name": name}, h)
	if r.status != http.StatusCreated {
		e.t.Fatalf("create app: %d %s", r.status, r.body)
	}
	var app api.App
	r.into(e.t, &app)
	return app
}

// createAPIKey mints a key for the app.
func (e *fullEnv) createAPIKey(access string, app api.App) api.ApiKeyCreated {
	e.t.Helper()
	h := bearer(access)
	h["Idempotency-Key"] = uuid.NewString()
	r := e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/api-keys", map[string]any{"name": "pos", "scopes": []string{"members:read"}}, h)
	if r.status != http.StatusCreated {
		e.t.Fatalf("create api key: %d %s", r.status, r.body)
	}
	var k api.ApiKeyCreated
	r.into(e.t, &k)
	return k
}

// enrollTOTP enrolls and confirms TOTP, returning the secret and the
// recovery codes. now is the instant the confirmation code is computed for.
func (e *fullEnv) enrollTOTP(access string, now time.Time) (string, []string) {
	e.t.Helper()
	h := bearer(access)
	r := e.do(http.MethodPost, "/v1/dashboard/me/totp/enroll", nil, h)
	if r.status != http.StatusOK {
		e.t.Fatalf("enroll: %d %s", r.status, r.body)
	}
	var enrollment api.TotpEnrollment
	r.into(e.t, &enrollment)
	r = e.do(http.MethodPost, "/v1/dashboard/me/totp/confirm", map[string]any{"code": totpCode(e.t, enrollment.Secret, now)}, h)
	if r.status != http.StatusOK {
		e.t.Fatalf("confirm: %d %s", r.status, r.body)
	}
	var confirmed api.TotpConfirmed
	r.into(e.t, &confirmed)
	return enrollment.Secret, confirmed.RecoveryCodes
}

// now returns the environment's current time.
func (e *fullEnv) now() time.Time {
	if e.clk != nil {
		return e.clk.Now()
	}
	return time.Now()
}
