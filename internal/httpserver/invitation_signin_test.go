package httpserver

import (
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

// inviteFor creates an invitation for email in the caller's first tenant and
// returns the raw token from the invite URL.
func (e *fullEnv) inviteFor(t *testing.T, access, email, role string) string {
	t.Helper()
	me := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(access))
	var profile api.Me
	me.into(t, &profile)
	tenantID := profile.Memberships[0].Tenant.Id.String()
	r := e.do(http.MethodPost, "/v1/dashboard/tenants/"+tenantID+"/invitations",
		map[string]any{"email": email, "role": role},
		map[string]string{"Authorization": "Bearer " + access, "Idempotency-Key": uuid.NewString()})
	if r.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", r.status, r.body)
	}
	var created api.InvitationCreated
	r.into(t, &created)
	_, token, ok := strings.Cut(created.InviteUrl, "token=")
	if !ok || token == "" {
		t.Fatalf("invite url without token: %s", created.InviteUrl)
	}
	return token
}

// Invariant (security review P1): an invitation for an address that already
// has an account never accepts a password. The link holder must be signed in
// as that account, which goes through the normal sign-in and its TOTP step.
func TestInvitationForExistingAccountRequiresSignIn(t *testing.T) {
	e := newFullEnv(t, fullOpts{})
	owner, _ := e.register("owner@example.com", "correct horse battery")
	invitee, _ := e.register("invitee@example.com", "invitee long password")
	token := e.inviteFor(t, owner.AccessToken, "invitee@example.com", "admin")

	// Right password, not signed in: refused, and the body says to sign in.
	r := e.accept(map[string]any{"token": token, "password": "invitee long password"})
	if r.status != http.StatusUnauthorized || !strings.Contains(string(r.body), "sign_in_required") {
		t.Fatalf("password acceptance for an existing account: %d %s", r.status, r.body)
	}
	// Wrong password: same status and code, so the link is not a password oracle.
	r2 := e.accept(map[string]any{"token": token, "password": "definitely not the password"})
	if r2.status != http.StatusUnauthorized || !strings.Contains(string(r2.body), "sign_in_required") {
		t.Fatalf("wrong password for an existing account: %d %s", r2.status, r2.body)
	}
	// Signed in as someone else: refused.
	r3 := e.do(http.MethodPost, "/v1/dashboard/auth/accept-invite", map[string]any{"token": token}, bearer(owner.AccessToken))
	if r3.status != http.StatusUnauthorized || !strings.Contains(string(r3.body), "sign_in_required") {
		t.Fatalf("acceptance by another signed-in user: %d %s", r3.status, r3.body)
	}
	// Signed in as the invitee: accepted, membership created, session issued.
	r4 := e.do(http.MethodPost, "/v1/dashboard/auth/accept-invite", map[string]any{"token": token}, bearer(invitee.AccessToken))
	if r4.status != http.StatusOK {
		t.Fatalf("signed-in acceptance: %d %s", r4.status, r4.body)
	}
	r4.refreshCookie(t)
	var sess api.Session
	r4.into(t, &sess)
	me := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(sess.AccessToken))
	var profile api.Me
	me.into(t, &profile)
	if len(profile.Memberships) != 2 {
		t.Fatalf("invitee should now belong to two tenants, got %d", len(profile.Memberships))
	}
	var joined bool
	for _, m := range profile.Memberships {
		if m.Role == api.RoleAdmin {
			joined = true
		}
	}
	if !joined {
		t.Errorf("admin membership in the inviting tenant missing: %+v", profile.Memberships)
	}
	// The token is single use.
	if r := e.do(http.MethodPost, "/v1/dashboard/auth/accept-invite", map[string]any{"token": token}, bearer(invitee.AccessToken)); r.status != http.StatusUnauthorized {
		t.Errorf("second acceptance: %d %s", r.status, r.body)
	}
}

// A TOTP-protected account cannot be entered through an invitation link with
// its password; the only path is the real sign-in.
func TestInvitationCannotBypassTOTP(t *testing.T) {
	e := newFullEnv(t, fullOpts{})
	owner, _ := e.register("owner@example.com", "correct horse battery")
	protected, _ := e.register("protected@example.com", "protected long password")
	e.enrollTOTP(protected.AccessToken, e.now())
	token := e.inviteFor(t, owner.AccessToken, "protected@example.com", "staff")

	r := e.accept(map[string]any{"token": token, "password": "protected long password"})
	if r.status != http.StatusUnauthorized {
		t.Fatalf("invitation bypassed TOTP: %d %s", r.status, r.body)
	}
	if len(r.cookies) != 0 {
		t.Error("a refused acceptance must not set a cookie")
	}
}

// A brand-new address still gets an account with its password; a signed-in
// stranger cannot claim it.
func TestInvitationForNewAddress(t *testing.T) {
	e := newFullEnv(t, fullOpts{})
	owner, _ := e.register("owner@example.com", "correct horse battery")
	token := e.inviteFor(t, owner.AccessToken, "newcomer@example.com", "staff")

	if r := e.do(http.MethodPost, "/v1/dashboard/auth/accept-invite", map[string]any{"token": token, "password": "a long enough password"}, bearer(owner.AccessToken)); r.status != http.StatusUnauthorized {
		t.Fatalf("signed-in stranger claimed a new-address invitation: %d %s", r.status, r.body)
	}
	if r := e.accept(map[string]any{"token": token}); r.status != http.StatusBadRequest {
		t.Fatalf("new address without password: %d %s", r.status, r.body)
	}
	r := e.accept(map[string]any{"token": token, "password": "a long enough password", "display_name": "New"})
	if r.status != http.StatusOK {
		t.Fatalf("new address acceptance: %d %s", r.status, r.body)
	}
	var sess api.Session
	r.into(t, &sess)
	if sess.User.Email != "newcomer@example.com" {
		t.Errorf("session for %s", sess.User.Email)
	}
}
