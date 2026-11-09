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
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

// Server implements api.StrictServerInterface.
type Server struct {
	cfg     *config.Config
	log     *slog.Logger
	db      *pgxpool.Pool
	version string
	started time.Time
	now     func() time.Time
}

// New returns a Server. db may be nil in unit tests; readiness then reports
// the database as unavailable.
func New(cfg *config.Config, log *slog.Logger, db *pgxpool.Pool, version string) *Server {
	return &Server{cfg: cfg, log: log, db: db, version: version, started: time.Now(), now: time.Now}
}

// Handler returns the HTTP handler with every route and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  s.requestError,
		ResponseErrorHandlerFunc: s.responseError,
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: s.requestError,
	})
	return chain(mux, recoverer(s.log), requestID, securityHeaders, logRequests(s.log))
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
