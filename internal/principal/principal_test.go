package principal

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		have, min Role
		want      bool
	}{
		{RoleOwner, RoleStaff, true},
		{RoleOwner, RoleOwner, true},
		{RoleAdmin, RoleOwner, false},
		{RoleStaff, RoleAdmin, false},
		{Role("bogus"), RoleStaff, false},
		{RoleOwner, Role("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.have.AtLeast(tc.min); got != tc.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tc.have, tc.min, got, tc.want)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("empty context should hold no principal")
	}
	p := Principal{Kind: KindAPIKey, APIKeyID: uuid.New(), Scopes: []string{"members:read"}}
	got, ok := FromContext(WithContext(context.Background(), p))
	if !ok || got.APIKeyID != p.APIKeyID || !got.HasScope("members:read") || got.HasScope("points:write") {
		t.Errorf("round trip failed: %+v %v", got, ok)
	}
	if !got.IsAPIKey() || got.IsUser() {
		t.Error("kind predicates wrong")
	}
}
