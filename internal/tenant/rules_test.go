package tenant

import (
	"strings"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

func TestDeriveSlug(t *testing.T) {
	cases := map[string]string{
		"Cafe Loyalty":      "cafe-loyalty",
		"  My  App!! 2026 ": "my-app-2026",
		"under_score":       "under-score",
		"UPPER":             "upper",
		"---":               "",
	}
	for in, want := range cases {
		got := deriveSlug(in)
		if want == "" {
			if !strings.HasPrefix(got, "app-") || len(got) != 10 {
				t.Errorf("deriveSlug(%q) = %q, want a random app- slug", in, got)
			}
			continue
		}
		if got != want {
			t.Errorf("deriveSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := deriveSlug("台北咖啡"); !strings.HasPrefix(got, "app-") {
		t.Errorf("non-ASCII name should get a random slug, got %q", got)
	}
	long := deriveSlug(strings.Repeat("a", 80))
	if len(long) != 50 || validateSlug(long) != nil {
		t.Errorf("long slug = %q (%d)", long, len(long))
	}
	if s := withSuffix(long); len(s) > 50 || validateSlug(s) != nil {
		t.Errorf("suffixed slug too long: %q", s)
	}
}

func TestValidateSlug(t *testing.T) {
	for _, ok := range []string{"abc", "my-app-2", "a1b"} {
		if err := validateSlug(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"ab", "-abc", "abc-", "Abc", "a b", strings.Repeat("a", 51)} {
		if err := validateSlug(bad); err == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestCanGrant(t *testing.T) {
	cases := []struct {
		actor, target principal.Role
		want          bool
	}{
		{principal.RoleOwner, principal.RoleOwner, true},
		{principal.RoleOwner, principal.RoleStaff, true},
		{principal.RoleAdmin, principal.RoleOwner, false},
		{principal.RoleAdmin, principal.RoleAdmin, true},
		{principal.RoleAdmin, principal.RoleStaff, true},
		{principal.RoleStaff, principal.RoleStaff, false},
		{principal.RoleOwner, principal.Role("boss"), false},
	}
	for _, tc := range cases {
		if got := canGrant(tc.actor, tc.target); got != tc.want {
			t.Errorf("canGrant(%s, %s) = %v, want %v", tc.actor, tc.target, got, tc.want)
		}
	}
}

func TestPlanAppLimit(t *testing.T) {
	if planAppLimit("free") != 1 || planAppLimit("agency") != 50 || planAppLimit("enterprise") != 0 || planAppLimit("weird") != 1 {
		t.Error("plan limits changed")
	}
}

func TestValidateScopes(t *testing.T) {
	got, err := validateScopes([]string{"points:write", "members:read", "points:write"})
	if err != nil || len(got) != 2 || got[0] != "members:read" {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := validateScopes(nil); err == nil {
		t.Error("empty scopes must fail")
	}
	if _, err := validateScopes([]string{"admin:all"}); err == nil {
		t.Error("unknown scope must fail")
	}
}

func TestInviteCode(t *testing.T) {
	code := randomInviteCode()
	if len(code) != 8 {
		t.Fatalf("len = %d", len(code))
	}
	for _, r := range code {
		if !strings.ContainsRune(inviteAlphabet, r) {
			t.Fatalf("unexpected character %q", r)
		}
	}
}

func TestValidators(t *testing.T) {
	if validateLocale("zh-TW") != nil || validateLocale("ja") != nil || validateLocale("en-US") != nil {
		t.Error("valid locales rejected")
	}
	if validateLocale("zh_TW") == nil || validateLocale("") == nil {
		t.Error("invalid locales accepted")
	}
	if validateStatus("active") != nil || validateStatus("paused") == nil {
		t.Error("status validation wrong")
	}
	if validateStaffRole("manager") != nil || validateStaffRole("boss") == nil {
		t.Error("staff role validation wrong")
	}
	if validateEnvironment("review") != nil || validateEnvironment("prod") == nil {
		t.Error("environment validation wrong")
	}
	if validateName("name", "  ", 10) == nil || validateName("name", "x", 10) != nil {
		t.Error("name validation wrong")
	}
}
