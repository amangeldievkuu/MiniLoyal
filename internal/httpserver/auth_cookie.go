package httpserver

import (
	"context"
	"net/http"
	"strings"
)

// The refresh token travels only in this cookie, scoped to the auth routes
// so it is never sent to the rest of the API.
const (
	refreshCookieName = "ml_refresh"
	refreshCookiePath = "/v1/dashboard/auth"
)

// refreshCookie returns the raw refresh token sent with the request, or "".
func (s *Server) refreshCookie(ctx context.Context) string {
	r := httpRequest(ctx)
	if r == nil {
		return ""
	}
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// setRefreshCookie stores token for the refresh token's lifetime.
func (s *Server) setRefreshCookie(ctx context.Context, token string) {
	maxAge := 30 * 24 * 60 * 60
	if s.auth != nil {
		maxAge = int(s.auth.RefreshTTL().Seconds())
	}
	s.writeRefreshCookie(ctx, token, maxAge)
}

// clearRefreshCookie removes the cookie from the browser.
func (s *Server) clearRefreshCookie(ctx context.Context) {
	s.writeRefreshCookie(ctx, "", -1)
}

func (s *Server) writeRefreshCookie(ctx context.Context, value string, maxAge int) {
	w := httpResponse(ctx)
	if w == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    value,
		Path:     refreshCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cookieSecure(httpRequest(ctx)),
		SameSite: http.SameSiteLaxMode,
	})
}

// cookieSecure marks cookies Secure when the client reached us over TLS, or
// the public address is https (the TLS proxy in front of nginx).
func (s *Server) cookieSecure(r *http.Request) bool {
	return s.requestIsHTTPS(r) || strings.HasPrefix(strings.ToLower(s.cfg.PublicBaseURL), "https://")
}
