package httpserver

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/staffauth"
)

// staffInvite creates a staff member in the app and returns it with its code.
func (e *memberEnv) staffInvite(access string, app api.App, name string) api.StaffCreated {
	e.t.Helper()
	var st api.StaffCreated
	e.must(e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/staff",
		map[string]any{"display_name": name, "role": "cashier"}, idemHeaders(access)), http.StatusCreated, "create staff").into(e.t, &st)
	if len(st.InviteCode) != 12 {
		e.t.Fatalf("invite code %q is not 12 characters", st.InviteCode)
	}
	return st
}

// staffToken registers a LIFF ID token for sub in the app's staff channel.
func (e *memberEnv) staffIDToken(app api.App, sub string) string {
	e.t.Helper()
	token := "staff-idtoken-" + sub + "-" + uuid.NewString()
	e.verifier.Add(token, lineauth.Claims{Sub: sub, Name: "Staff " + sub, Aud: e.staffChannelOf(app)})
	return token
}

// staffLogin signs a LINE account in to the staff app.
func (e *memberEnv) staffLogin(app api.App, sub, code string) resp {
	e.t.Helper()
	body := map[string]any{"app_id": app.Id, "id_token": e.staffIDToken(app, sub)}
	if code != "" {
		body["invite_code"] = code
	}
	return e.do(http.MethodPost, "/v1/staff/auth/line", body, nil)
}

func (e *memberEnv) staffSession(app api.App, sub, code string) api.StaffSession {
	e.t.Helper()
	var sess api.StaffSession
	e.must(e.staffLogin(app, sub, code), http.StatusOK, "staff login").into(e.t, &sess)
	return sess
}

func errCode(t *testing.T, r resp) api.Error {
	t.Helper()
	var e api.Error
	r.into(t, &e)
	return e
}

func TestStaffSignInBindsWithTheInviteCode(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	store := e.must(e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/stores", map[string]any{"name": "Zhongxiao"}, idemHeaders(owner)), http.StatusCreated, "store")
	var st api.Store
	store.into(t, &st)

	var mei api.StaffCreated
	e.must(e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/staff",
		map[string]any{"display_name": "Mei", "role": "manager", "store_id": st.Id}, idemHeaders(owner)), http.StatusCreated, "staff").into(t, &mei)

	// Typed sloppily: lower case with dashes and spaces still matches.
	typed := strings.ToLower(mei.InviteCode[:4] + "-" + mei.InviteCode[4:8] + " " + mei.InviteCode[8:])
	sess := e.staffSession(app, "Ustaff1", typed)
	if sess.Staff.Id != mei.Id || sess.Staff.Role != api.Manager || sess.App.Id != app.Id || sess.TokenType != api.StaffSessionTokenTypeBearer {
		t.Fatalf("session: %+v", sess)
	}
	if sess.Staff.Store == nil || sess.Staff.Store.Name != "Zhongxiao" || sess.ExpiresIn != int((12*time.Hour).Seconds()) {
		t.Fatalf("store or lifetime: %+v", sess)
	}
	var me api.StaffMe
	e.must(e.do(http.MethodGet, "/v1/staff/me", nil, bearer(sess.SessionToken)), http.StatusOK, "staff me").into(t, &me)
	if me.Id != mei.Id || me.Store == nil {
		t.Errorf("me: %+v", me)
	}

	// Later sign-ins need only the LINE account.
	e.staffSession(app, "Ustaff1", "")

	// The staff list shows the link and the last sign-in.
	var list []api.Staff
	e.must(e.do(http.MethodGet, "/v1/dashboard/apps/"+app.Id.String()+"/staff", nil, bearer(owner)), http.StatusOK, "list").into(t, &list)
	if len(list) != 1 || !list[0].LineLinked || list[0].LastLoginAt == nil {
		t.Errorf("staff list: %+v", list)
	}
	audit := e.must(e.do(http.MethodGet, "/v1/dashboard/tenants/"+e.tenantID(owner)+"/audit-log", nil, bearer(owner)), http.StatusOK, "audit")
	if !strings.Contains(string(audit.body), "staff.login_bound") {
		t.Error("binding was not audited")
	}
}

func TestStaffSignInRefusalsLookAlike(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	otherOwner := e.owner("other@example.com")
	other := e.app(otherOwner)

	used := e.staffInvite(owner, app, "Used")
	e.staffSession(app, "Ubound", used.InviteCode)
	expiring := e.staffInvite(owner, app, "Expiring")
	foreign := e.staffInvite(otherOwner, other, "Foreign")

	refusals := map[string]resp{
		"no code":        e.staffLogin(app, "Unobody", ""),
		"wrong code":     e.staffLogin(app, "Unobody", "ABCDEFGHJKLM"),
		"reused code":    e.staffLogin(app, "Usecond", used.InviteCode),
		"other app code": e.staffLogin(app, "Uforeign", foreign.InviteCode),
	}
	// The other app's code still works there.
	e.staffSession(other, "Uforeign", foreign.InviteCode)
	e.clk.Advance(8 * 24 * time.Hour)
	refusals["expired code"] = e.staffLogin(app, "Ulate", expiring.InviteCode)

	var first string
	for name, r := range refusals {
		if r.status != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
		msg := errCode(t, r).Message
		if first == "" {
			first = msg
		} else if msg != first {
			t.Errorf("%s answers %q, others %q", name, msg, first)
		}
		if strings.Contains(string(r.body), other.Id.String()) {
			t.Errorf("%s names the other app", name)
		}
	}
}

func TestStaffInviteAttemptsAreRateLimitedPerApp(t *testing.T) {
	e := newMemberEnvWith(t, staffauth.Config{IPPerMinute: 10000, InvitePerMinute: 3})
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	for i := 0; i < 3; i++ {
		if r := e.staffLogin(app, "Uguess", "WRONGWRONG23"); r.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i, r.status, r.body)
		}
	}
	r := e.staffLogin(app, "Uguess", "WRONGWRONG23")
	if r.status != http.StatusTooManyRequests || r.headers.Get("Retry-After") == "" {
		t.Fatalf("fourth attempt: %d %s", r.status, r.body)
	}
	// Sign-ins without a code are not counted against the invite limit.
	st := e.staffInvite(owner, app, "Mei")
	e.clk.Advance(time.Minute)
	e.staffSession(app, "Umei", st.InviteCode)
	for i := 0; i < 5; i++ {
		e.staffSession(app, "Umei", "")
	}
}

func TestDisabledStaffAndTokenKindsAreRefused(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	staff := e.staffSession(app, "Umei", st.InviteCode)
	member := e.lineLogin(app, "Umember", "Lin")

	for name, c := range map[string]struct {
		path  string
		token string
	}{
		"staff token on member route":    {"/v1/me", staff.SessionToken},
		"staff token on dashboard route": {"/v1/dashboard/me", staff.SessionToken},
		"member token on staff route":    {"/v1/staff/me", member.SessionToken},
		"dashboard token on staff route": {"/v1/staff/me", owner},
	} {
		if r := e.do(http.MethodGet, c.path, nil, bearer(c.token)); r.status != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, r.status)
		}
	}
	// An API key cannot sign in as staff either.
	if r := e.do(http.MethodPost, "/v1/staff/scan", map[string]any{"qr": "ml1.xxxxxxxxxxxx"}, map[string]string{"X-API-Key": "ml_nope"}); r.status != http.StatusUnauthorized {
		t.Errorf("API key on staff route: %d", r.status)
	}

	e.must(e.do(http.MethodPatch, "/v1/dashboard/staff/"+st.Id.String(), map[string]any{"status": "disabled"}, idemHeaders(owner)), http.StatusOK, "disable")
	if r := e.do(http.MethodGet, "/v1/staff/me", nil, bearer(staff.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("disabled staff session: %d", r.status)
	}
	if r := e.staffLogin(app, "Umei", ""); r.status != http.StatusUnauthorized {
		t.Errorf("disabled staff login: %d", r.status)
	}
}

func TestResetStaffInviteUnlinksAndEndsSessions(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	old := e.staffSession(app, "Uoldphone", st.InviteCode)

	e.clk.Advance(time.Second)
	key := idemHeaders(owner)
	r := e.must(e.do(http.MethodPost, "/v1/dashboard/staff/"+st.Id.String()+"/invite", nil, key), http.StatusOK, "reset")
	var fresh api.StaffCreated
	r.into(t, &fresh)
	if len(fresh.InviteCode) != 12 || fresh.InviteCode == st.InviteCode || fresh.LineLinked {
		t.Fatalf("reset: %+v", fresh)
	}
	// Idempotent replay returns the same code.
	var replay api.StaffCreated
	e.must(e.do(http.MethodPost, "/v1/dashboard/staff/"+st.Id.String()+"/invite", nil, key), http.StatusOK, "replay").into(t, &replay)
	if replay.InviteCode != fresh.InviteCode {
		t.Error("replay issued another code")
	}

	if r := e.do(http.MethodGet, "/v1/staff/me", nil, bearer(old.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("old session after reset: %d", r.status)
	}
	if r := e.staffLogin(app, "Uoldphone", ""); r.status != http.StatusUnauthorized {
		t.Errorf("old LINE account after reset: %d", r.status)
	}
	if r := e.staffLogin(app, "Unewphone", st.InviteCode); r.status != http.StatusUnauthorized {
		t.Errorf("old code after reset: %d", r.status)
	}
	sess := e.staffSession(app, "Unewphone", fresh.InviteCode)
	if sess.Staff.Id != st.Id {
		t.Errorf("new phone bound to %s", sess.Staff.Id)
	}
	audit := e.must(e.do(http.MethodGet, "/v1/dashboard/tenants/"+e.tenantID(owner)+"/audit-log", nil, bearer(owner)), http.StatusOK, "audit")
	if !strings.Contains(string(audit.body), "staff.invite_reset") {
		t.Error("reset not audited")
	}
	if strings.Contains(string(audit.body), fresh.InviteCode) || strings.Contains(e.logs.String(), fresh.InviteCode) {
		t.Error("the invite code leaked into the audit log or logs")
	}
}

func TestMemberCardScanFlow(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	staff := e.staffSession(app, "Ustaff", st.InviteCode)
	member := e.lineLogin(app, "Umember", "Lin")

	var c api.MemberCard
	e.must(e.do(http.MethodGet, "/v1/me/card", nil, bearer(member.SessionToken)), http.StatusOK, "card").into(t, &c)
	if !strings.HasPrefix(c.Qr, "ml1.") || c.WindowSeconds != 30 || c.RefreshInSeconds < 1 || c.RefreshInSeconds > 30 {
		t.Fatalf("card: %+v", c)
	}

	// A balance shows up in the scan.
	e.db.Exec(t, "INSERT INTO points_accounts (member_id, tenant_id, app_id, balance, lifetime_earned) VALUES ($1, $2, $3, 42, 42)",
		uuid.UUID(member.Member.Id), uuid.MustParse(e.tenantID(owner)), uuid.UUID(app.Id))

	var res api.ScanResult
	e.must(e.do(http.MethodPost, "/v1/staff/scan", map[string]any{"qr": c.Qr}, bearer(staff.SessionToken)), http.StatusOK, "scan").into(t, &res)
	if res.Member.Id != member.Member.Id || res.Balance != 42 || res.Member.DisplayName == nil || *res.Member.DisplayName != "Lin" {
		t.Fatalf("scan: %+v", res)
	}
	claims, err := e.cards.VerifyScanToken(res.ScanToken)
	if err != nil || claims.StaffID != uuid.UUID(st.Id) || claims.MemberID != uuid.UUID(member.Member.Id) || claims.AppID != uuid.UUID(app.Id) {
		t.Fatalf("scan token: %+v %v", claims, err)
	}
	if strings.Contains(e.logs.String(), c.Qr) || strings.Contains(e.logs.String(), res.ScanToken) {
		t.Error("QR payload or scan token logged")
	}

	// Members cannot scan and a scan needs the staff session.
	if r := e.do(http.MethodPost, "/v1/staff/scan", map[string]any{"qr": c.Qr}, bearer(member.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("member scanning: %d", r.status)
	}
	if r := e.do(http.MethodGet, "/v1/me/card", nil, bearer(staff.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("staff reading a member card: %d", r.status)
	}
}

func TestScanRefusals(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	otherApp := e.app(e.owner("other@example.com"))
	st := e.staffInvite(owner, app, "Mei")
	staff := e.staffSession(app, "Ustaff", st.InviteCode)

	card := func(token string) string {
		t.Helper()
		var c api.MemberCard
		e.must(e.do(http.MethodGet, "/v1/me/card", nil, bearer(token)), http.StatusOK, "card").into(t, &c)
		return c.Qr
	}
	scan := func(qr string) resp {
		t.Helper()
		return e.do(http.MethodPost, "/v1/staff/scan", map[string]any{"qr": qr}, bearer(staff.SessionToken))
	}
	expect := func(name string, r resp, code string) {
		t.Helper()
		if r.status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, r.status, r.body)
			return
		}
		if got := errCode(t, r).Code; got != code {
			t.Errorf("%s: code %q, want %q", name, got, code)
		}
		if strings.Contains(string(r.body), otherApp.Id.String()) {
			t.Errorf("%s: names the other app", name)
		}
	}

	foreigner := e.lineLogin(otherApp, "Uforeign", "Foreign")
	expect("other app's card", scan(card(foreigner.SessionToken)), "invalid_card")
	expect("garbage", scan("ml1.not-a-real-card-code"), "invalid_card")
	expect("wrong prefix", scan("https://example.com/qr/123"), "invalid_card")

	rotated := e.lineLogin(app, "Urotated", "Rotated")
	oldQR := card(rotated.SessionToken)
	key := idemHeaders(owner)
	var rot api.MemberCardRotated
	e.must(e.do(http.MethodPost, "/v1/dashboard/members/"+rotated.Member.Id.String()+"/card/rotate", nil, key), http.StatusOK, "rotate").into(t, &rot)
	if rot.QrKeyVersion != 2 {
		t.Errorf("rotated version %d", rot.QrKeyVersion)
	}
	var replay api.MemberCardRotated
	e.must(e.do(http.MethodPost, "/v1/dashboard/members/"+rotated.Member.Id.String()+"/card/rotate", nil, key), http.StatusOK, "rotate replay").into(t, &replay)
	if replay.QrKeyVersion != 2 {
		t.Errorf("replayed rotation bumped the version again: %d", replay.QrKeyVersion)
	}
	expect("card before rotation", scan(oldQR), "invalid_card")
	e.must(scan(card(rotated.SessionToken)), http.StatusOK, "card after rotation")

	erased := e.lineLogin(app, "Uerased", "Erased")
	erasedQR := card(erased.SessionToken)
	e.must(e.do(http.MethodDelete, "/v1/me", nil, bearer(erased.SessionToken)), http.StatusNoContent, "erase")
	expect("erased member", scan(erasedQR), "invalid_card")

	screenshot := e.lineLogin(app, "Ushot", "Shot")
	shotQR := card(screenshot.SessionToken)
	e.clk.Advance(90 * time.Second)
	expect("90-second-old code", scan(shotQR), "card_expired")
	e.must(scan(card(screenshot.SessionToken)), http.StatusOK, "fresh code")
}

func TestRotateMemberCardNeedsAdmin(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	member := e.lineLogin(app, "Umember", "Lin")
	staffUser := e.teamUser(owner, "staffer@example.com", "staff")
	if r := e.do(http.MethodPost, "/v1/dashboard/members/"+member.Member.Id.String()+"/card/rotate", nil, idemHeaders(staffUser)); r.status != http.StatusForbidden {
		t.Errorf("staff role rotating a card: %d %s", r.status, r.body)
	}
	outsider := e.owner("outsider@example.com")
	if r := e.do(http.MethodPost, "/v1/dashboard/members/"+member.Member.Id.String()+"/card/rotate", nil, idemHeaders(outsider)); r.status != http.StatusNotFound {
		t.Errorf("other tenant rotating a card: %d", r.status)
	}
}

// teamUser registers a dashboard user and adds them to owner's tenant with role.
func (e *memberEnv) teamUser(owner, email, role string) string {
	e.t.Helper()
	access := e.owner(email)
	var me api.Me
	e.must(e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(access)), http.StatusOK, "me").into(e.t, &me)
	e.db.Exec(e.t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3)", uuid.MustParse(e.tenantID(owner)), uuid.UUID(me.User.Id), role)
	return access
}

func TestEndMemberSessions(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	member := e.lineLogin(app, "Umember", "Lin")
	e.must(e.do(http.MethodGet, "/v1/me", nil, bearer(member.SessionToken)), http.StatusOK, "me before")

	e.clk.Advance(1500 * time.Microsecond)
	var updated api.App
	e.must(e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/member-sessions/end", nil, idemHeaders(owner)), http.StatusOK, "end").into(t, &updated)
	if updated.MemberSessionsValidFrom == nil {
		t.Fatal("member_sessions_valid_from not set")
	}
	if r := e.do(http.MethodGet, "/v1/me", nil, bearer(member.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("old member session: %d", r.status)
	}
	// A sign-in in the same instant is still valid; so is a later one.
	again := e.lineLogin(app, "Umember", "Lin")
	e.must(e.do(http.MethodGet, "/v1/me", nil, bearer(again.SessionToken)), http.StatusOK, "new session")

	staffUser := e.teamUser(owner, "staffer@example.com", "staff")
	if r := e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/member-sessions/end", nil, idemHeaders(staffUser)); r.status != http.StatusForbidden {
		t.Errorf("staff role ending sessions: %d", r.status)
	}
	audit := e.must(e.do(http.MethodGet, "/v1/dashboard/tenants/"+e.tenantID(owner)+"/audit-log", nil, bearer(owner)), http.StatusOK, "audit")
	if !strings.Contains(string(audit.body), "app.member_sessions_end") {
		t.Error("not audited")
	}
}

func TestHS256IsOptInPerApp(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	lastSecrets := func() map[string]string {
		t.Helper()
		return e.verifier.Calls[len(e.verifier.Calls)-1].ChannelSecrets
	}

	if app.AllowHs256IdTokens == nil || *app.AllowHs256IdTokens {
		t.Fatalf("new app: allow_hs256_id_tokens = %v", app.AllowHs256IdTokens)
	}
	e.lineLogin(app, "Umember", "Lin")
	if len(lastSecrets()) != 0 {
		t.Error("member sign-in passed channel secrets without opt-in")
	}
	e.staffSession(app, "Ustaff", st.InviteCode)
	if len(lastSecrets()) != 0 {
		t.Error("staff sign-in passed channel secrets without opt-in")
	}

	var updated api.App
	e.must(e.do(http.MethodPatch, "/v1/dashboard/apps/"+app.Id.String(), map[string]any{"allow_hs256_id_tokens": true}, idemHeaders(owner)), http.StatusOK, "opt in").into(t, &updated)
	if updated.AllowHs256IdTokens == nil || !*updated.AllowHs256IdTokens {
		t.Fatalf("after opt-in: %+v", updated.AllowHs256IdTokens)
	}
	e.lineLogin(app, "Umember", "Lin")
	if lastSecrets()[e.channelOf(app)] != "channel-secret" {
		t.Errorf("member sign-in after opt-in: %v", lastSecrets())
	}
	e.staffSession(app, "Ustaff", "")
	if lastSecrets()[e.staffChannelOf(app)] != "channel-secret" {
		t.Errorf("staff sign-in after opt-in: %v", lastSecrets())
	}
	audit := e.must(e.do(http.MethodGet, "/v1/dashboard/tenants/"+e.tenantID(owner)+"/audit-log", nil, bearer(owner)), http.StatusOK, "audit")
	if !strings.Contains(string(audit.body), "allow_hs256_id_tokens") {
		t.Error("toggle not audited")
	}
	if strings.Contains(string(audit.body), "channel-secret") {
		t.Error("secret in audit log")
	}
}

func TestStaffRoleSeesRedactedMembers(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	member := e.lineLogin(app, "Uprivate", "Private")
	e.must(e.do(http.MethodPatch, "/v1/me", map[string]any{
		"phone": "+886 912 345 678", "birthday": "1990-05-01", "custom_fields": map[string]any{"tier": "gold"},
	}, idemHeaders(member.SessionToken)), http.StatusOK, "profile")
	staffUser := e.teamUser(owner, "staffer@example.com", "staff")
	adminUser := e.teamUser(owner, "admin@example.com", "admin")
	path := "/v1/dashboard/members/" + member.Member.Id.String()
	listPath := "/v1/dashboard/apps/" + app.Id.String() + "/members"

	var full api.MemberDetail
	e.must(e.do(http.MethodGet, path, nil, bearer(adminUser)), http.StatusOK, "admin get").into(t, &full)
	if deref(full.LineUserId) != "Uprivate" || full.Phone == nil || full.Birthday == nil || full.CustomFields["tier"] != "gold" {
		t.Fatalf("admin view: %+v", full)
	}

	var redacted api.MemberDetail
	r := e.must(e.do(http.MethodGet, path, nil, bearer(staffUser)), http.StatusOK, "staff get")
	r.into(t, &redacted)
	var page api.MemberPage
	l := e.must(e.do(http.MethodGet, listPath, nil, bearer(staffUser)), http.StatusOK, "staff list")
	l.into(t, &page)
	if len(page.Items) != 1 {
		t.Fatalf("staff list: %+v", page)
	}
	for name, m := range map[string]api.MemberDetail{"get": redacted, "list": page.Items[0]} {
		if m.LineUserId != nil || m.Phone != nil || m.Birthday != nil || len(m.CustomFields) != 0 {
			t.Errorf("staff %s view leaks personal data: %+v", name, m)
		}
		if m.DisplayName == nil || *m.DisplayName != "Private" {
			t.Errorf("staff %s view lost the display name", name)
		}
	}
	for _, raw := range [][]byte{r.body, l.body} {
		for _, secret := range []string{`"line_user_id"`, "912 345", "1990-05-01", "gold"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("staff response contains %q", secret)
			}
		}
	}
	// The owner still sees everything in the list.
	var ownerPage api.MemberPage
	e.must(e.do(http.MethodGet, listPath, nil, bearer(owner)), http.StatusOK, "owner list").into(t, &ownerPage)
	if deref(ownerPage.Items[0].LineUserId) != "Uprivate" {
		t.Error("owner list redacted")
	}
}
