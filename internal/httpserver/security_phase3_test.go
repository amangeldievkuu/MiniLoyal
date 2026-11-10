package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Security review Phase 3: HTTP-level tests, each failing before its fix.

// F3: the staff app signs in only through its own LINE Login channel; a
// member's MINI App token never opens it, and a staff-channel token is never
// a member sign-in.
func TestStaffSignInNeedsTheStaffChannel(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")

	// A member token (MINI App audience) at staff sign-in.
	memberToken := "member-idtoken-" + uuid.NewString()
	e.verifier.Add(memberToken, lineauth.Claims{Sub: "Ustaffmember", Name: "Mei", Aud: e.channelOf(app)})
	r := e.do(http.MethodPost, "/v1/staff/auth/line", map[string]any{"app_id": app.Id, "id_token": memberToken, "invite_code": st.InviteCode}, nil)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("member token at staff sign-in: %d %s", r.status, r.body)
	}
	// A staff-channel token at member sign-in.
	staffToken := e.staffIDToken(app, "Ustaffmember")
	r = e.do(http.MethodPost, "/v1/auth/line", map[string]any{"app_id": app.Id, "id_token": staffToken}, nil)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("staff token at member sign-in: %d %s", r.status, r.body)
	}
	// The staff channel's ID must differ from the MINI App's.
	r = e.do(http.MethodPut, "/v1/dashboard/apps/"+app.Id.String()+"/channels/staff",
		map[string]any{"channel_id": e.channelOf(app), "liff_id": "9999999999-staffapp2", "channel_secret": "channel-secret"}, idemHeaders(owner))
	if r.status != http.StatusBadRequest {
		t.Fatalf("staff channel sharing the MINI App channel ID: %d %s", r.status, r.body)
	}
	// Without a staff channel, staff sign-in is refused with a clear code.
	e.must(e.do(http.MethodDelete, "/v1/dashboard/apps/"+app.Id.String()+"/channels/staff", nil, bearer(owner)), http.StatusNoContent, "remove staff channel")
	r = e.staffLogin(app, "Ustaff", st.InviteCode)
	if r.status != http.StatusConflict || errCode(t, r).Code != "staff_channel_required" {
		t.Fatalf("no staff channel: %d %s", r.status, r.body)
	}
}

// F9: disabling a staff member ends their sessions; re-enabling them does
// not revive the old token.
func TestDisablingStaffEndsTheirSessions(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	sess := e.staffSession(app, "Ustaff", st.InviteCode)
	staffPath := "/v1/dashboard/staff/" + st.Id.String()

	e.clk.Advance(time.Second)
	e.must(e.do(http.MethodPatch, staffPath, map[string]any{"status": "disabled"}, idemHeaders(owner)), http.StatusOK, "disable")
	e.must(e.do(http.MethodPatch, staffPath, map[string]any{"status": "active"}, idemHeaders(owner)), http.StatusOK, "enable")
	if r := e.do(http.MethodGet, "/v1/staff/me", nil, bearer(sess.SessionToken)); r.status != http.StatusUnauthorized {
		t.Fatalf("token from before the disable: %d %s", r.status, r.body)
	}
	// A new sign-in works.
	fresh := e.staffSession(app, "Ustaff", "")
	e.must(e.do(http.MethodGet, "/v1/staff/me", nil, bearer(fresh.SessionToken)), http.StatusOK, "new session")

	// Demoting a manager ends their sessions too.
	e.must(e.do(http.MethodPatch, staffPath, map[string]any{"role": "manager"}, idemHeaders(owner)), http.StatusOK, "promote")
	e.clk.Advance(time.Second)
	mgr := e.staffSession(app, "Ustaff", "")
	e.clk.Advance(time.Second)
	e.must(e.do(http.MethodPatch, staffPath, map[string]any{"role": "cashier"}, idemHeaders(owner)), http.StatusOK, "demote")
	if r := e.do(http.MethodGet, "/v1/staff/me", nil, bearer(mgr.SessionToken)); r.status != http.StatusUnauthorized {
		t.Fatalf("manager token after the demotion: %d %s", r.status, r.body)
	}
}

// F11: invite codes are stored as keyed HMACs, not plain SHA-256.
func TestInviteCodesAreStoredKeyed(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	var stored string
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT invite_code_hash FROM staff WHERE id = $1", uuid.UUID(st.Id)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256([]byte(tenant.NormalizeInviteCode(st.InviteCode)))
	if stored == hex.EncodeToString(plain[:]) {
		t.Fatal("invite code stored as plain SHA-256")
	}
	if stored != tenant.HashInviteCodeWithKey(tenant.InviteKey(fullSigningKey), st.InviteCode) {
		t.Fatal("invite code not stored as the keyed HMAC")
	}
	// And the code still binds.
	e.staffSession(app, "Ustaff", st.InviteCode)
}

// F13: the staff role cannot use member search to confirm LINE user IDs.
func TestStaffRoleCannotSearchByLineUserID(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	e.lineLogin(app, "Uexactmatch0001", "Lin")
	staffUser := e.teamUser(owner, "staff@example.com", "staff")

	page, r := e.listMembers(staffUser, app, url.Values{"q": {"Uexactmatch0001"}})
	if r.status != http.StatusOK || len(page.Items) != 0 {
		t.Fatalf("staff role search by LINE user ID: %d, %d items", r.status, len(page.Items))
	}
	page, r = e.listMembers(staffUser, app, url.Values{"q": {"Lin"}})
	if r.status != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("staff role search by name: %d, %d items", r.status, len(page.Items))
	}
	page, _ = e.listMembers(owner, app, url.Values{"q": {"Uexactmatch0001"}})
	if len(page.Items) != 1 {
		t.Fatalf("owner search by LINE user ID: %d items", len(page.Items))
	}
}

// F14: a code whose MAC does not verify is invalid_card even when its window
// is old, so card_expired reveals nothing about member IDs.
func TestScanChecksTheMACBeforeTheWindow(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	staff := e.staffSession(app, "Ustaff", st.InviteCode)
	m := e.lineLogin(app, "Umember", "Lin")

	forger, err := card.New(bytes.Repeat([]byte{3}, 32), clock.At(e.clk.Now().Add(-10*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	old, err := forger.Card(uuid.UUID(app.Id), uuid.UUID(m.Member.Id), 1)
	if err != nil {
		t.Fatal(err)
	}
	r := e.do(http.MethodPost, "/v1/staff/scan", map[string]any{"qr": old.Payload}, bearer(staff.SessionToken))
	if r.status != http.StatusUnprocessableEntity || errCode(t, r).Code != "invalid_card" {
		t.Fatalf("forged old code: %d %s, want invalid_card", r.status, r.body)
	}
}

// F14: switching HS256 off also ends member sessions.
func TestSwitchingHS256OffEndsMemberSessions(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	appPath := "/v1/dashboard/apps/" + app.Id.String()
	e.must(e.do(http.MethodPatch, appPath, map[string]any{"allow_hs256_id_tokens": true}, idemHeaders(owner)), http.StatusOK, "on")
	sess := e.lineLogin(app, "Umember", "Lin")
	e.clk.Advance(time.Second)
	var updated api.App
	e.must(e.do(http.MethodPatch, appPath, map[string]any{"allow_hs256_id_tokens": false}, idemHeaders(owner)), http.StatusOK, "off").into(t, &updated)
	if updated.MemberSessionsValidFrom == nil {
		t.Fatal("member_sessions_valid_from not set when HS256 was switched off")
	}
	if r := e.do(http.MethodGet, "/v1/me", nil, bearer(sess.SessionToken)); r.status != http.StatusUnauthorized {
		t.Fatalf("member session from before the switch: %d %s", r.status, r.body)
	}
}
