package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Env: config.Development}
	s := New(Deps{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	s.started = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return s.started.Add(90 * time.Second) }
	return s
}

func TestHealthz(t *testing.T) {
	h := newTestServer(t).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body api.Health
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != api.Ok || body.Version != "test" || body.UptimeSeconds != 90 {
		t.Errorf("unexpected body: %+v", body)
	}
	if rec.Header().Get("X-Request-ID") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing headers: %v", rec.Header())
	}
}

func TestReadyzWithoutDatabase(t *testing.T) {
	h := newTestServer(t).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Header.Set("X-Request-ID", "req-123")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	var body api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "unavailable" || body.RequestId != "req-123" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	h := newTestServer(t).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestBearerWithoutAuthenticatorIs401(t *testing.T) {
	h := newTestServer(t).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/me", nil)
	req.Header.Set("Authorization", "Bearer abc")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAPIKeyWithoutAuthenticatorIs401(t *testing.T) {
	h := newTestServer(t).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/app", nil)
	req.Header.Set("X-API-Key", "ml_abc")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestCaptureHTTPExposesRequest(t *testing.T) {
	s := newTestServer(t)
	var seen *http.Request
	var ua string
	probe := func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, _ any) (any, error) {
		seen = httpRequest(ctx)
		ua = userAgent(ctx)
		return nil, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("User-Agent", "probe/1.0")
	_, _ = captureHTTP(probe, "probe")(req.Context(), httptest.NewRecorder(), req, nil)
	if seen == nil || ua != "probe/1.0" {
		t.Fatalf("request not captured: %v, ua %q", seen, ua)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	if s.requestIsHTTPS(req) {
		t.Error("X-Forwarded-Proto must be ignored unless TRUST_PROXY is set")
	}
	s.cfg.TrustProxy = true
	if !s.requestIsHTTPS(req) {
		t.Error("X-Forwarded-Proto should be honoured behind the trusted proxy")
	}
}
