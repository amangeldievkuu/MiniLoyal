// Package httpserver assembles the HTTP API: generated strict handlers from
// api/openapi.yaml, plus middleware shared by every route.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/member"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Deps are the collaborators the server needs. Phase packages add their
// services here and cmd/api wires them.
type Deps struct {
	Config  *config.Config
	Log     *slog.Logger
	DB      *pgxpool.Pool
	Version string

	// Auth serves the dashboard sign-in endpoints. Nil answers them 503.
	Auth *auth.Service
	// Dashboard verifies dashboard access tokens. Defaults to Auth; nil
	// with no Auth means every bearer token is rejected.
	Dashboard DashboardAuthenticator
	// APIKeys verifies server API keys (internal/tenant). Nil means every
	// X-API-Key is rejected.
	APIKeys APIKeyAuthenticator
	// Members verifies member session tokens (internal/lineauth). Nil means
	// every bearer token outside /v1/dashboard/ is rejected.
	Members MemberAuthenticator
	// Tenants serves tenants, team, apps, channels, stores, staff, API keys
	// and the audit log.
	Tenants *tenant.Service
	// LineAuth serves member sign-in (POST /v1/auth/line). It also verifies
	// member sessions, so Members defaults to it.
	LineAuth *lineauth.Service
	// MemberSvc serves the member's profile, consent, export and erasure, and
	// the member lists for the dashboard and the server API.
	MemberSvc *member.Service
}

// Server implements api.StrictServerInterface.
type Server struct {
	cfg       *config.Config
	log       *slog.Logger
	db        *pgxpool.Pool
	version   string
	auth      *auth.Service
	dashboard DashboardAuthenticator
	apiKeys   APIKeyAuthenticator
	members   MemberAuthenticator
	tenants   *tenant.Service
	lineauth  *lineauth.Service
	memberSvc *member.Service
	started   time.Time
	now       func() time.Time
}

// New returns a Server. Deps.DB may be nil in unit tests; readiness then
// reports the database as unavailable.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Config == nil {
		d.Config = &config.Config{Env: config.Development}
	}
	if d.Dashboard == nil && d.Auth != nil {
		d.Dashboard = d.Auth
	}
	if d.Members == nil && d.LineAuth != nil {
		d.Members = d.LineAuth
	}
	return &Server{
		cfg:       d.Config,
		log:       d.Log,
		db:        d.DB,
		version:   d.Version,
		auth:      d.Auth,
		dashboard: d.Dashboard,
		apiKeys:   d.APIKeys,
		members:   d.Members,
		tenants:   d.Tenants,
		lineauth:  d.LineAuth,
		memberSvc: d.MemberSvc,
		started:   time.Now(),
		now:       time.Now,
	}
}

// Handler returns the HTTP handler with every route and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(s, []api.StrictMiddlewareFunc{captureHTTP}, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  s.requestError,
		ResponseErrorHandlerFunc: s.responseError,
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: s.requestError,
	})
	// Cookie-authenticated routes (refresh, logout) must not be reachable by
	// cross-site form posts; browsers announce those with Sec-Fetch-Site.
	cop := http.NewCrossOriginProtection()
	if origin := s.cfg.DashboardOrigin; origin != "" {
		if err := cop.AddTrustedOrigin(origin); err != nil {
			s.log.Warn("DASHBOARD_ORIGIN is not a valid origin; cross-origin requests from it are refused", "origin", origin, "err", err)
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusForbidden, "cross_origin", "cross-origin request refused")
	}))
	return chain(cop.Handler(mux), recoverer(s.log), requestID, securityHeaders, logRequests(s.log), limitBody(maxBodyBytes), s.authenticate)
}

// GetHealthz reports liveness: the process is up and serving.
func (s *Server) GetHealthz(context.Context, api.GetHealthzRequestObject) (api.GetHealthzResponseObject, error) {
	return api.GetHealthz200JSONResponse(s.health()), nil
}

// GetReadyz reports readiness: the database answers.
func (s *Server) GetReadyz(ctx context.Context, _ api.GetReadyzRequestObject) (api.GetReadyzResponseObject, error) {
	if s.db == nil {
		return api.GetReadyz503JSONResponse(s.unavailable(ctx, "database not configured")), nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.db.Ping(pingCtx); err != nil {
		s.log.WarnContext(ctx, "readiness: database ping failed", "err", err)
		return api.GetReadyz503JSONResponse(s.unavailable(ctx, "database unavailable")), nil
	}
	return api.GetReadyz200JSONResponse(s.health()), nil
}

func (s *Server) health() api.Health {
	return api.Health{
		Status:        api.Ok,
		Version:       s.version,
		UptimeSeconds: int64(s.now().Sub(s.started).Seconds()),
	}
}

func (s *Server) unavailable(ctx context.Context, msg string) api.Error {
	return api.Error{Code: "unavailable", Message: msg, RequestId: RequestIDFrom(ctx)}
}
