package httpserver

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// DashboardAuthenticator verifies a dashboard access token. Implemented by
// the auth service.
type DashboardAuthenticator interface {
	AuthenticateDashboard(ctx context.Context, bearerToken string) (principal.Principal, error)
}

// APIKeyAuthenticator verifies a server API key. Implemented by the tenant
// service, which owns api_keys.
type APIKeyAuthenticator interface {
	AuthenticateAPIKey(ctx context.Context, key string) (principal.Principal, error)
}

// MemberAuthenticator verifies a MINI App member session token. Implemented
// by the lineauth service.
type MemberAuthenticator interface {
	AuthenticateMember(ctx context.Context, bearerToken string) (principal.Principal, error)
}

// StaffAuthenticator verifies a staff app session token. Implemented by the
// staffauth service.
type StaffAuthenticator interface {
	AuthenticateStaff(ctx context.Context, bearerToken string) (principal.Principal, error)
}

// Bearer tokens are dispatched by path: /v1/dashboard/* carries dashboard
// sessions, /v1/staff/* staff sessions (except the staff sign-in itself),
// everything else member sessions. Each token kind carries its own typ claim,
// so a token presented on the wrong side is refused either way.
const (
	dashboardPathPrefix = "/v1/dashboard/"
	staffPathPrefix     = "/v1/staff/"
	staffSignInPath     = "/v1/staff/auth/line"
)

// authenticate reads X-API-Key or Authorization: Bearer, verifies it and
// stores the principal in the context. Requests without credentials pass
// through; handlers decide what they require via requireUser and
// requireAPIKey. Invalid credentials are rejected here with 401.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
			if s.apiKeys == nil {
				writeError(w, r, http.StatusUnauthorized, "unauthorized", "API keys are not enabled")
				return
			}
			p, err := s.apiKeys.AuthenticateAPIKey(ctx, key)
			if err != nil {
				s.writeAuthFailure(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(principal.WithContext(ctx, p)))
			return
		}
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			if token == "" {
				writeError(w, r, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
				return
			}
			var p principal.Principal
			var err error
			switch {
			case strings.HasPrefix(r.URL.Path, staffPathPrefix) && r.URL.Path != staffSignInPath:
				if s.staff == nil {
					writeError(w, r, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
					return
				}
				p, err = s.staff.AuthenticateStaff(ctx, token)
			case strings.HasPrefix(r.URL.Path, dashboardPathPrefix):
				if s.dashboard == nil {
					writeError(w, r, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
					return
				}
				p, err = s.dashboard.AuthenticateDashboard(ctx, token)
			default:
				if s.members == nil {
					writeError(w, r, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
					return
				}
				p, err = s.members.AuthenticateMember(ctx, token)
			}
			if err != nil {
				s.writeAuthFailure(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(principal.WithContext(ctx, p)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeAuthFailure(w http.ResponseWriter, r *http.Request, err error) {
	if ae, ok := apierr.As(err); ok {
		writeErrorDetails(w, r, ae)
		return
	}
	s.log.ErrorContext(r.Context(), "authentication failed unexpectedly", "err", err)
	writeError(w, r, http.StatusInternalServerError, "internal", "internal server error")
}

// requireUser returns the dashboard user calling, or 401.
func requireUser(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok || !p.IsUser() {
		return principal.Principal{}, apierr.Unauthorized("sign in to the dashboard")
	}
	return p, nil
}

// requireStaff returns the staff member calling, or 401.
//
//lint:ignore U1000 used by the staff handlers that replace stubs_staff.go
func requireStaff(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok || !p.IsStaff() {
		return principal.Principal{}, apierr.Unauthorized("sign in to the staff app")
	}
	return p, nil
}

// requireMember returns the MINI App member calling, or 401.
//
//lint:ignore U1000 used by the member handlers that replace stubs_member.go
func requireMember(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok || !p.IsMember() {
		return principal.Principal{}, apierr.Unauthorized("sign in with LINE")
	}
	return p, nil
}

// requireAPIKey returns the API key calling, or 401; with a non-empty scope
// it also checks the key carries it, or 403.
func requireAPIKey(ctx context.Context, scope string) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok || !p.IsAPIKey() {
		return principal.Principal{}, apierr.Unauthorized("send a valid X-API-Key")
	}
	if scope != "" && !p.HasScope(scope) {
		return principal.Principal{}, apierr.Forbidden("API key lacks the " + scope + " scope")
	}
	return p, nil
}

// clientIP returns the caller's address. Behind the trusted reverse proxy it
// reads the LAST X-Forwarded-For entry: nginx appends the peer it saw, so
// that entry is the only one the client cannot choose. Without a trusted
// proxy the header is ignored and the TCP peer is used.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
