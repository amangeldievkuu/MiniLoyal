package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

// requestError answers malformed requests (bad path or query parameters,
// unreadable bodies) with the API's error envelope.
func (s *Server) requestError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusBadRequest
	var invalid *api.InvalidParamFormatError
	var required *api.RequiredParamError
	switch {
	case errors.As(err, &invalid), errors.As(err, &required):
		status = http.StatusBadRequest
	}
	writeError(w, r, status, "bad_request", err.Error())
}

// responseError answers handler failures. The cause is logged, never shown.
func (s *Server) responseError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "handler failed", "err", err, "path", r.URL.Path)
	writeError(w, r, http.StatusInternalServerError, "internal", "internal server error")
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Error{Code: code, Message: msg, RequestId: RequestIDFrom(r.Context())})
}
