package httpserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

func TestClientIPUsesTheLastForwardedHop(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:4321"
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9")

	if got := s.clientIP(req); got != "10.0.0.5" {
		t.Errorf("without TRUST_PROXY = %q, want the TCP peer", got)
	}
	s.cfg.TrustProxy = true
	if got := s.clientIP(req); got != "203.0.113.9" {
		t.Errorf("with TRUST_PROXY = %q, want the last entry", got)
	}
	req.Header.Set("X-Forwarded-For", "garbage")
	if got := s.clientIP(req); got != "10.0.0.5" {
		t.Errorf("unparseable header = %q, want the TCP peer", got)
	}
}

func TestOversizedBodyIs413(t *testing.T) {
	h := newTestServer(t).Handler()
	// Valid JSON up to the limit, so the decoder reads past it instead of
	// failing on syntax first.
	body := append(append([]byte(`{"email":"`), bytes.Repeat([]byte("a"), maxBodyBytes+1)...), []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "payload_too_large") {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	s := New(Deps{Config: &config.Config{Env: config.Development, DashboardOrigin: "http://localhost:5173"}, Version: "test"})
	h := s.Handler()

	post := func(headers map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/auth/refresh", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := post(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); got != http.StatusForbidden {
		t.Errorf("cross-site = %d, want 403", got)
	}
	if got := post(map[string]string{"Sec-Fetch-Site": "same-origin"}); got == http.StatusForbidden {
		t.Error("same-origin request was refused")
	}
	if got := post(map[string]string{"Origin": "http://localhost:5173", "Sec-Fetch-Site": "cross-site"}); got == http.StatusForbidden {
		t.Error("the trusted dashboard origin was refused")
	}
	if got := post(nil); got == http.StatusForbidden {
		t.Error("a request without fetch metadata (server to server) was refused")
	}
}

func TestClientRequestIDIsSanitized(t *testing.T) {
	h := newTestServer(t).Handler()
	cases := map[string]bool{ // header value -> echoed back unchanged
		"req-123_ABC":                      true,
		"has space":                        false,
		"<script>alert(1)</script>":        false,
		strings.Repeat("x", 65):            false,
		"4b7f2e3a9c1d4e5f8a6b7c8d9e0f1a2b": true,
	}
	for id, echoed := range cases {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-ID", id)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("X-Request-ID")
		if echoed && got != id {
			t.Errorf("%q was replaced with %q", id, got)
		}
		if !echoed && (got == id || got == "") {
			t.Errorf("%q should have been replaced, got %q", id, got)
		}
	}
}

func TestWebhookPostsWithoutFetchMetadataAreNotRefusedAsCrossOrigin(t *testing.T) {
	h := newTestServer(t).Handler()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/line/00000000-0000-0000-0000-000000000001", strings.NewReader(`{"events":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", "c2ln")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("LINE webhook delivery refused as cross-origin: %s", rec.Body)
	}
}
