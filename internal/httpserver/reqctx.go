package httpserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

type httpCtxKey struct{}

type httpPair struct {
	w http.ResponseWriter
	r *http.Request
}

// captureHTTP is a strict middleware that keeps the raw request and response
// writer reachable from the strict handlers, which only receive a context.
// Handlers use it for cookies, the client IP and the user agent.
func captureHTTP(f api.StrictHandlerFunc, _ string) api.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		ctx = context.WithValue(ctx, httpCtxKey{}, httpPair{w: w, r: r})
		return f(ctx, w, r.WithContext(ctx), request)
	}
}

// httpRequest returns the request behind ctx, or nil outside a handler.
func httpRequest(ctx context.Context) *http.Request {
	p, _ := ctx.Value(httpCtxKey{}).(httpPair)
	return p.r
}

// httpResponse returns the response writer behind ctx, or nil outside a
// handler. Set headers and cookies on it before returning the response.
func httpResponse(ctx context.Context) http.ResponseWriter {
	p, _ := ctx.Value(httpCtxKey{}).(httpPair)
	return p.w
}

// userAgent returns a bounded User-Agent for session records.
func userAgent(ctx context.Context) string {
	r := httpRequest(ctx)
	if r == nil {
		return ""
	}
	ua := strings.TrimSpace(r.UserAgent())
	if len(ua) > 200 {
		ua = ua[:200]
	}
	return ua
}

// requestIsHTTPS reports whether the client reached us over TLS, honouring
// X-Forwarded-Proto only behind the trusted proxy.
func (s *Server) requestIsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if s.cfg.TrustProxy {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			return strings.EqualFold(strings.TrimSpace(proto), "https")
		}
	}
	return r.TLS != nil
}
