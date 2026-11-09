// Package apierr defines the client-visible error. Services return it (or
// wrap it) and the HTTP layer renders it as the Error envelope from the
// OpenAPI document. Anything that is not an *Error is a 500 whose cause is
// logged and never shown.
package apierr

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Error is a failure the client should see.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	// Cause is logged, never shown.
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return e.Code + ": " + e.Message
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// New builds an error with an explicit status and code.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WithDetails returns a copy carrying extra fields for the client.
func (e *Error) WithDetails(details map[string]any) *Error {
	c := *e
	c.Details = details
	return &c
}

// WithCause returns a copy carrying an internal cause for the logs.
func (e *Error) WithCause(err error) *Error {
	c := *e
	c.Cause = err
	return &c
}

// As extracts an *Error from err.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Common constructors. Messages are safe to show to clients.

func BadRequest(message string) *Error {
	return New(http.StatusBadRequest, "bad_request", message)
}

func Unauthorized(message string) *Error {
	if message == "" {
		message = "authentication required"
	}
	return New(http.StatusUnauthorized, "unauthorized", message)
}

func Forbidden(message string) *Error {
	if message == "" {
		message = "not allowed"
	}
	return New(http.StatusForbidden, "forbidden", message)
}

func NotFound(what string) *Error {
	if what == "" {
		what = "resource"
	}
	return New(http.StatusNotFound, "not_found", what+" not found")
}

func Conflict(message string) *Error {
	return New(http.StatusConflict, "conflict", message)
}

// IdempotencyMismatch answers a reused Idempotency-Key with a different payload.
func IdempotencyMismatch() *Error {
	return New(http.StatusUnprocessableEntity, "idempotency_mismatch",
		"Idempotency-Key was already used with a different request")
}

// Validation answers a well-formed request whose content is invalid, with
// one message per field.
func Validation(fields map[string]string) *Error {
	details := make(map[string]any, len(fields))
	for k, v := range fields {
		details[k] = v
	}
	return New(http.StatusBadRequest, "validation_failed", "request is invalid").WithDetails(details)
}

// RateLimited tells the client when to retry.
func RateLimited(retryAfter time.Duration) *Error {
	secs := int(retryAfter.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return New(http.StatusTooManyRequests, "rate_limited", "too many requests").
		WithDetails(map[string]any{"retry_after_seconds": secs})
}

// Unavailable reports a dependency outage.
func Unavailable(message string) *Error {
	return New(http.StatusServiceUnavailable, "unavailable", message)
}
