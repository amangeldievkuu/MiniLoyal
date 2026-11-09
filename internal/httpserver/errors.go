package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
)

// errNotImplemented marks a stubbed operation; it answers 501.
var errNotImplemented = errors.New("not implemented")

// requestError answers malformed requests (bad path or query parameters,
// unreadable bodies) with the API's error envelope.
func (s *Server) requestError(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
}

// responseError answers handler failures. An *apierr.Error is shown to the
// client as is; anything else is logged and hidden behind a 500.
func (s *Server) responseError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotImplemented) {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "this operation is not available yet")
		return
	}
	if ae, ok := apierr.As(err); ok {
		if ae.Status >= 500 {
			s.log.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path, "status", ae.Status)
		} else if ae.Cause != nil {
			s.log.WarnContext(r.Context(), "request rejected", "err", err, "path", r.URL.Path, "status", ae.Status)
		}
		writeErrorDetails(w, r, ae)
		return
	}
	s.log.ErrorContext(r.Context(), "handler failed", "err", err, "path", r.URL.Path)
	writeError(w, r, http.StatusInternalServerError, "internal", "internal server error")
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeErrorDetails(w, r, &apierr.Error{Status: status, Code: code, Message: msg})
}

func writeErrorDetails(w http.ResponseWriter, r *http.Request, e *apierr.Error) {
	body := api.Error{Code: e.Code, Message: e.Message, RequestId: RequestIDFrom(r.Context())}
	if len(e.Details) > 0 {
		details := e.Details
		body.Details = &details
		if secs, ok := details["retry_after_seconds"].(int); ok && e.Status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(body)
}
