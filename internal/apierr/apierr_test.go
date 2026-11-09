package apierr

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAsThroughWrapping(t *testing.T) {
	base := NotFound("app")
	wrapped := fmt.Errorf("service: %w", base)
	got, ok := As(wrapped)
	if !ok || got.Status != 404 || got.Code != "not_found" {
		t.Fatalf("As = %+v, %v", got, ok)
	}
}

func TestWithCauseKeepsOriginalIntact(t *testing.T) {
	base := Forbidden("")
	cause := errors.New("role staff")
	withCause := base.WithCause(cause)
	if base.Cause != nil {
		t.Error("WithCause mutated the original")
	}
	if !errors.Is(withCause, cause) {
		t.Error("cause not reachable through errors.Is")
	}
	if withCause.Message != "not allowed" {
		t.Errorf("default message = %q", withCause.Message)
	}
}

func TestRateLimitedRoundsUp(t *testing.T) {
	e := RateLimited(300 * time.Millisecond)
	if e.Details["retry_after_seconds"] != 1 {
		t.Errorf("retry_after_seconds = %v", e.Details["retry_after_seconds"])
	}
}

func TestValidationCarriesFields(t *testing.T) {
	e := Validation(map[string]string{"email": "invalid"})
	if e.Status != 400 || e.Details["email"] != "invalid" {
		t.Errorf("unexpected: %+v", e)
	}
}
