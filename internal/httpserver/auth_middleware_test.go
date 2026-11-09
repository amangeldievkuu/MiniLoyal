package httpserver

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

func TestRequireUser(t *testing.T) {
	if _, err := requireUser(context.Background()); statusOf(err) != 401 {
		t.Errorf("no principal: %v", err)
	}
	key := principal.Principal{Kind: principal.KindAPIKey, APIKeyID: uuid.New()}
	if _, err := requireUser(principal.WithContext(context.Background(), key)); statusOf(err) != 401 {
		t.Errorf("api key as user: %v", err)
	}
	user := principal.Principal{Kind: principal.KindUser, UserID: uuid.New()}
	got, err := requireUser(principal.WithContext(context.Background(), user))
	if err != nil || got.UserID != user.UserID {
		t.Errorf("user: %v %+v", err, got)
	}
}

func TestRequireAPIKey(t *testing.T) {
	if _, err := requireAPIKey(context.Background(), ""); statusOf(err) != 401 {
		t.Errorf("no principal: %v", err)
	}
	user := principal.Principal{Kind: principal.KindUser, UserID: uuid.New()}
	if _, err := requireAPIKey(principal.WithContext(context.Background(), user), ""); statusOf(err) != 401 {
		t.Errorf("user as api key: %v", err)
	}
	key := principal.Principal{Kind: principal.KindAPIKey, APIKeyID: uuid.New(), Scopes: []string{"members:read"}}
	ctx := principal.WithContext(context.Background(), key)
	if _, err := requireAPIKey(ctx, ""); err != nil {
		t.Errorf("no scope required: %v", err)
	}
	if _, err := requireAPIKey(ctx, "members:read"); err != nil {
		t.Errorf("granted scope: %v", err)
	}
	if _, err := requireAPIKey(ctx, "points:write"); statusOf(err) != 403 {
		t.Errorf("missing scope: %v", err)
	}
}

func statusOf(err error) int {
	if err == nil {
		return 200
	}
	if ae, ok := apierr.As(err); ok {
		return ae.Status
	}
	return -1
}
