package httpserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
)

const dashboardOrigin = "http://localhost:5173"

func newInviteEnv(t *testing.T) (*fullEnv, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	e := newFullEnv(t, fullOpts{
		cfg: config.Config{DashboardOrigin: dashboardOrigin},
		log: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return e, &logs
}

func (e *fullEnv) invite(access string, tenantID uuid.UUID, email, role, key string) resp {
	e.t.Helper()
	h := bearer(access)
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return e.do(http.MethodPost, "/v1/dashboard/tenants/"+tenantID.String()+"/invitations", map[string]any{"email": email, "role": role}, h)
}

func inviteToken(t *testing.T, r resp) (api.InvitationCreated, string) {
	t.Helper()
	if r.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", r.status, r.body)
	}
	var created api.InvitationCreated
	r.into(t, &created)
	u, err := url.Parse(created.InviteUrl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.InviteUrl, dashboardOrigin+"/accept-invite?token=") {
		t.Fatalf("invite_url = %s", created.InviteUrl)
	}
	return created, u.Query().Get("token")
}

func (e *fullEnv) accept(body map[string]any) resp {
	e.t.Helper()
	return e.do(http.MethodPost, "/v1/dashboard/auth/accept-invite", body, nil)
}

func TestInvitationRolesAndConflicts(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)

	// Owner invites an admin, who accepts with a new account.
	_, adminToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "Admin@Example.com", "admin", uuid.NewString()))
	adminAccept := e.accept(map[string]any{"token": adminToken, "password": "admin long password", "display_name": "Ada"})
	if adminAccept.status != http.StatusOK {
		t.Fatalf("admin accept: %d %s", adminAccept.status, adminAccept.body)
	}
	var adminSess api.Session
	adminAccept.into(t, &adminSess)
	adminAccept.refreshCookie(t)

	// Admins invite admins and staff, never owners; staff invite nobody.
	if r := e.invite(adminSess.AccessToken, tn.Id, "boss@example.com", "owner", uuid.NewString()); r.status != http.StatusForbidden {
		t.Errorf("admin grants owner: %d %s", r.status, r.body)
	}
	_, staffToken := inviteToken(t, e.invite(adminSess.AccessToken, tn.Id, "staff@example.com", "staff", uuid.NewString()))
	staffAccept := e.accept(map[string]any{"token": staffToken, "password": "staff long password"})
	if staffAccept.status != http.StatusOK {
		t.Fatalf("staff accept: %d %s", staffAccept.status, staffAccept.body)
	}
	var staffSess api.Session
	staffAccept.into(t, &staffSess)
	if r := e.invite(staffSess.AccessToken, tn.Id, "x@example.com", "staff", uuid.NewString()); r.status != http.StatusForbidden {
		t.Errorf("staff invites: %d %s", r.status, r.body)
	}

	// Members and pending invitations are refused.
	if r := e.invite(owner.AccessToken, tn.Id, "admin@example.com", "staff", uuid.NewString()); r.status != http.StatusConflict {
		t.Errorf("existing member: %d %s", r.status, r.body)
	}
	inviteToken(t, e.invite(owner.AccessToken, tn.Id, "pending@example.com", "staff", uuid.NewString()))
	if r := e.invite(owner.AccessToken, tn.Id, "pending@example.com", "admin", uuid.NewString()); r.status != http.StatusConflict {
		t.Errorf("duplicate pending: %d %s", r.status, r.body)
	}
	if r := e.invite(owner.AccessToken, tn.Id, "not-an-email", "staff", uuid.NewString()); r.status != http.StatusBadRequest {
		t.Errorf("bad email: %d %s", r.status, r.body)
	}

	// The team now has three members with the invited roles.
	team := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/team", nil, bearer(owner.AccessToken))
	var members []api.TeamMember
	team.into(t, &members)
	roles := map[string]string{}
	for _, m := range members {
		roles[string(m.Email)] = string(m.Role)
	}
	if roles["admin@example.com"] != "admin" || roles["staff@example.com"] != "staff" || roles["owner@example.com"] != "owner" {
		t.Errorf("team roles = %v", roles)
	}
}

func TestInvitationListRevokeAndExpiry(t *testing.T) {
	e, logs := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	listPath := "/v1/dashboard/tenants/" + tn.Id.String() + "/invitations"

	accepted, acceptedToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "accepted@example.com", "staff", uuid.NewString()))
	revoked, revokedToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "revoked@example.com", "staff", uuid.NewString()))
	expiring, expiringToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "expiring@example.com", "staff", uuid.NewString()))

	// The list shows pending invitations and never the token.
	list := e.do(http.MethodGet, listPath, nil, bearer(owner.AccessToken))
	if list.status != http.StatusOK {
		t.Fatalf("list: %d %s", list.status, list.body)
	}
	var pending []api.Invitation
	list.into(t, &pending)
	if len(pending) != 3 {
		t.Fatalf("pending = %d, want 3", len(pending))
	}
	for _, tok := range []string{acceptedToken, revokedToken, expiringToken} {
		if bytes.Contains(list.body, []byte(tok)) {
			t.Error("the list leaks an invitation token")
		}
		if bytes.Contains(logs.Bytes(), []byte(tok)) {
			t.Error("the logs contain an invitation token")
		}
	}

	if r := e.accept(map[string]any{"token": acceptedToken, "password": "long enough password"}); r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	if r := e.accept(map[string]any{"token": acceptedToken, "password": "long enough password"}); r.status != http.StatusUnauthorized {
		t.Errorf("second accept of a used token: %d %s", r.status, r.body)
	}

	// Revoke, then the link is dead; revoking again is 404.
	if r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+revoked.Id.String(), nil, bearer(owner.AccessToken)); r.status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+revoked.Id.String(), nil, bearer(owner.AccessToken)); r.status != http.StatusNotFound {
		t.Errorf("revoke twice: %d %s", r.status, r.body)
	}
	if r := e.accept(map[string]any{"token": revokedToken, "password": "long enough password"}); r.status != http.StatusUnauthorized {
		t.Errorf("accept revoked: %d %s", r.status, r.body)
	}

	// Expiry follows the service clock. The owner's 15-minute session has
	// expired with it, so they sign in again.
	e.clk.Advance(8 * 24 * time.Hour)
	if r := e.accept(map[string]any{"token": expiringToken, "password": "long enough password"}); r.status != http.StatusUnauthorized {
		t.Errorf("accept expired: %d %s", r.status, r.body)
	}
	relogin, r := e.login("owner@example.com", "correct horse battery", nil)
	if r.status != http.StatusOK || relogin.Session == nil {
		t.Fatalf("re-login: %d %s", r.status, r.body)
	}
	owner = *relogin.Session
	list = e.do(http.MethodGet, listPath, nil, bearer(owner.AccessToken))
	list.into(t, &pending)
	if len(pending) != 0 {
		t.Errorf("pending after accept, revoke and expiry = %d, want 0: %s", len(pending), list.body)
	}
	_ = accepted
	_ = expiring
	// An expired pending invitation does not block a new one for the address.
	inviteToken(t, e.invite(owner.AccessToken, tn.Id, "expiring@example.com", "staff", uuid.NewString()))

	// Unknown tokens and malformed bodies.
	if r := e.accept(map[string]any{"token": strings.Repeat("x", 43), "password": "long enough password"}); r.status != http.StatusUnauthorized {
		t.Errorf("unknown token: %d %s", r.status, r.body)
	}
}

func TestInvitationAcceptCreatesOrConfirmsTheAccount(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	other, _ := e.register("other@example.com", "other long password")

	// New address: the password is required, then the account and the
	// membership exist and the response is a signed-in session with cookie.
	_, newToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "new@example.com", "admin", uuid.NewString()))
	missing := e.accept(map[string]any{"token": newToken})
	if missing.status != http.StatusBadRequest || !bytes.Contains(missing.body, []byte(`"password"`)) {
		t.Fatalf("missing password: %d %s", missing.status, missing.body)
	}
	if r := e.accept(map[string]any{"token": newToken, "password": "short"}); r.status != http.StatusBadRequest {
		t.Errorf("short password: %d %s", r.status, r.body)
	}
	ok := e.accept(map[string]any{"token": newToken, "password": "a long enough password"})
	if ok.status != http.StatusOK {
		t.Fatalf("accept new: %d %s", ok.status, ok.body)
	}
	ok.refreshCookie(t)
	var sess api.Session
	ok.into(t, &sess)
	if string(sess.User.Email) != "new@example.com" {
		t.Errorf("session user = %s", sess.User.Email)
	}
	me := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(sess.AccessToken))
	var profile api.Me
	me.into(t, &profile)
	if len(profile.Memberships) != 1 || profile.Memberships[0].Tenant.Id != tn.Id || profile.Memberships[0].Role != "admin" {
		t.Errorf("memberships = %+v", profile.Memberships)
	}
	// The new account can sign in with that password.
	if res, r := e.login("new@example.com", "a long enough password", nil); r.status != http.StatusOK || res.Session == nil {
		t.Errorf("login after accept: %d %s", r.status, r.body)
	}

	// Existing address: the right password is required; no new account.
	_, otherToken := inviteToken(t, e.invite(owner.AccessToken, tn.Id, "other@example.com", "staff", uuid.NewString()))
	if r := e.accept(map[string]any{"token": otherToken, "password": "wrong password here"}); r.status != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d %s", r.status, r.body)
	}
	if r := e.accept(map[string]any{"token": otherToken}); r.status != http.StatusBadRequest {
		t.Fatalf("no password for existing account: %d %s", r.status, r.body)
	}
	joined := e.accept(map[string]any{"token": otherToken, "password": "other long password"})
	if joined.status != http.StatusOK {
		t.Fatalf("accept existing: %d %s", joined.status, joined.body)
	}
	var joinedSess api.Session
	joined.into(t, &joinedSess)
	if joinedSess.User.Id != other.User.Id {
		t.Errorf("accept created a second account: %v vs %v", joinedSess.User.Id, other.User.Id)
	}
	me = e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(joinedSess.AccessToken))
	me.into(t, &profile)
	if len(profile.Memberships) != 2 {
		t.Errorf("other should now belong to two tenants: %+v", profile.Memberships)
	}

	// The security history shows the acceptance.
	events := e.do(http.MethodGet, "/v1/dashboard/me/security-events", nil, bearer(joinedSess.AccessToken))
	if events.status != http.StatusOK {
		t.Fatalf("security events: %d %s", events.status, events.body)
	}
	var page api.SecurityEventPage
	events.into(t, &page)
	seen := map[string]bool{}
	for _, ev := range page.Items {
		seen[ev.Event] = true
	}
	if !seen["invite.accepted"] || !seen["login.failed"] {
		t.Errorf("security events = %v", seen)
	}
	for _, tok := range []string{otherToken, newToken} {
		if bytes.Contains(events.body, []byte(tok)) {
			t.Error("security events leak an invitation token")
		}
	}
}

func TestInvitationIdempotencyAndTenantScope(t *testing.T) {
	e, _ := newInviteEnv(t)
	owner, _ := e.register("owner@example.com", "correct horse battery")
	tn := e.tenantOf(owner.AccessToken)
	ownerB, _ := e.register("owner-b@example.com", "correct horse battery")

	key := uuid.NewString()
	first := e.invite(owner.AccessToken, tn.Id, "idem@example.com", "staff", key)
	replay := e.invite(owner.AccessToken, tn.Id, "idem@example.com", "staff", key)
	if first.status != http.StatusCreated || replay.status != http.StatusCreated {
		t.Fatalf("first %d, replay %d: %s", first.status, replay.status, replay.body)
	}
	if !bytes.Equal(first.body, replay.body) {
		t.Errorf("replay body differs (invite_url must be stable):\n%s\n%s", first.body, replay.body)
	}
	if r := e.invite(owner.AccessToken, tn.Id, "idem@example.com", "admin", key); r.status != http.StatusUnprocessableEntity {
		t.Errorf("mismatch: %d %s", r.status, r.body)
	}
	created, _ := inviteToken(t, first)

	// Another tenant's owner sees and touches nothing.
	if r := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/invitations", nil, bearer(ownerB.AccessToken)); r.status != http.StatusNotFound {
		t.Errorf("outsider list: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodDelete, "/v1/dashboard/invitations/"+created.Id.String(), nil, bearer(ownerB.AccessToken)); r.status != http.StatusNotFound {
		t.Errorf("outsider revoke: %d %s", r.status, r.body)
	}
	if r := e.invite(ownerB.AccessToken, tn.Id, "x@example.com", "staff", uuid.NewString()); r.status != http.StatusNotFound {
		t.Errorf("outsider invite: %d %s", r.status, r.body)
	}
	// Still pending for the real owner.
	list := e.do(http.MethodGet, "/v1/dashboard/tenants/"+tn.Id.String()+"/invitations", nil, bearer(owner.AccessToken))
	var pending []api.Invitation
	list.into(t, &pending)
	if len(pending) != 1 {
		t.Errorf("pending = %d, want 1", len(pending))
	}
}

func TestSecurityEventsEndpointPagesAndIsPrivate(t *testing.T) {
	e, _ := newInviteEnv(t)
	a, _ := e.register("a@example.com", "correct horse battery")
	b, _ := e.register("b@example.com", "correct horse battery")
	for i := 0; i < 3; i++ {
		e.clk.Advance(time.Second)
		if _, r := e.login("a@example.com", "correct horse battery", nil); r.status != http.StatusOK {
			t.Fatalf("login: %d %s", r.status, r.body)
		}
	}
	first := e.do(http.MethodGet, "/v1/dashboard/me/security-events?limit=2", nil, bearer(a.AccessToken))
	if first.status != http.StatusOK {
		t.Fatalf("events: %d %s", first.status, first.body)
	}
	var page api.SecurityEventPage
	first.into(t, &page)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("first page: %d items, cursor %v", len(page.Items), page.NextCursor)
	}
	second := e.do(http.MethodGet, "/v1/dashboard/me/security-events?limit=2&cursor="+url.QueryEscape(*page.NextCursor), nil, bearer(a.AccessToken))
	var page2 api.SecurityEventPage
	second.into(t, &page2)
	if len(page2.Items) != 1 || page2.NextCursor != nil {
		t.Errorf("second page: %d items, cursor %v", len(page2.Items), page2.NextCursor)
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/me/security-events?cursor=not-a-cursor!", nil, bearer(a.AccessToken)); r.status != http.StatusBadRequest {
		t.Errorf("bad cursor: %d", r.status)
	}
	bEvents := e.do(http.MethodGet, "/v1/dashboard/me/security-events", nil, bearer(b.AccessToken))
	var bPage api.SecurityEventPage
	bEvents.into(t, &bPage)
	if len(bPage.Items) != 0 {
		t.Errorf("b sees %d events after only registering", len(bPage.Items))
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/me/security-events", nil, nil); r.status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", r.status)
	}
}
