package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/auth/authtest"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/coupon"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth/lineauthtest"
	"github.com/amangeldievkuu/miniloyal/internal/member"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/staffauth"
	"github.com/amangeldievkuu/miniloyal/internal/stamp"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
	"github.com/amangeldievkuu/miniloyal/internal/webhook"
)

// memberEnv wires auth, tenant, lineauth, member, staff and points services behind the
// handler with a fake LINE verifier, the way cmd/api does.
type memberEnv struct {
	t        *testing.T
	db       *testutil.DB
	clk      *lockedClock
	verifier *lineauthtest.FakeVerifier
	handler  http.Handler
	logs     *bytes.Buffer
	seq      int
	// channels maps an app ID to the channel ID registered for it.
	channels map[string]string
	// staffChannels maps an app ID to its staff app LINE Login channel.
	staffChannels map[string]string
	// cards is the codec behind /v1/me/card and /v1/staff/scan.
	cards *card.Codec
}

const memberChannelID = "2007009999"

func newMemberEnv(t *testing.T) *memberEnv {
	t.Helper()
	return newMemberEnvWith(t, staffauth.Config{IPPerMinute: 10000, InvitePerMinute: 10000})
}

// newMemberEnvWith is newMemberEnv with explicit staff app limits.
func newMemberEnvWith(t *testing.T, staffCfg staffauth.Config) *memberEnv {
	t.Helper()
	d := testutil.NewDB(t)
	e := &memberEnv{t: t, db: d, verifier: lineauthtest.NewFakeVerifier(), logs: &bytes.Buffer{}, channels: map[string]string{}, staffChannels: map[string]string{}}
	e.clk = newLockedClock(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	logger := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	keyring := crypto.Static(bytes.Repeat([]byte{7}, 32))

	authSvc, err := auth.New(auth.Deps{Pool: d.App, Clock: e.clk, Log: logger, Encryptor: authtest.PlainEncryptor{},
		Config: auth.Config{SigningKey: fullSigningKey, IPPerMinute: 10000, EmailPerMinute: 10000, UserPerMinute: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	tenants := tenant.New(d.App, keyring, e.clk, logger)
	tenants.SetInviteKey(fullSigningKey)
	lineAuth, err := lineauth.New(lineauth.Deps{Pool: d.App, Keyring: keyring, Verifier: e.verifier, Clock: e.clk, Log: logger,
		Config: lineauth.Config{SigningKey: fullSigningKey, IPPerMinute: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	members := member.New(member.Deps{Pool: d.App, Tenants: tenants, Clock: e.clk, Log: logger})
	e.cards, err = card.New(fullSigningKey, e.clk)
	if err != nil {
		t.Fatal(err)
	}
	staffCfg.SigningKey = fullSigningKey
	staffSvc, err := staffauth.New(staffauth.Deps{Pool: d.App, LineAuth: lineAuth, Card: e.cards, Clock: e.clk, Log: logger, Config: staffCfg})
	if err != nil {
		t.Fatal(err)
	}
	staffLimit := access.NewStaffLimiter(e.clk, 0)
	e.handler = New(Deps{
		Config:    &config.Config{Env: config.Development, PublicBaseURL: "http://localhost:8080"},
		Log:       logger,
		DB:        d.App,
		Version:   "test",
		Auth:      authSvc,
		APIKeys:   tenants,
		Tenants:   tenants,
		LineAuth:  lineAuth,
		MemberSvc: members,
		Webhooks:  webhook.New(d.App, tenants, logger),
		Card:      e.cards,
		StaffAuth: staffSvc,
		Points:    points.New(points.Deps{Pool: d.App, Tenants: tenants, Keyring: keyring, Card: e.cards, Clock: e.clk, Log: logger, StaffLimiter: staffLimit}),
		Coupons:   coupon.New(coupon.Deps{Pool: d.App, Tenants: tenants, Keyring: keyring, Card: e.cards, Clock: e.clk, Log: logger, StaffLimiter: staffLimit}),
		Stamps:    stamp.New(stamp.Deps{Pool: d.App, Tenants: tenants, Keyring: keyring, Card: e.cards, Clock: e.clk, Log: logger, StaffLimiter: staffLimit}),
	}).Handler()
	return e
}

func (e *memberEnv) do(method, path string, body any, headers map[string]string) resp {
	e.t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		buf = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, buf)
	req.RemoteAddr = "203.0.113.7:51000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: b, headers: res.Header, cookies: res.Cookies()}
}

// raw serves one request with a pre-encoded body, for webhook deliveries
// whose signature covers the exact bytes.
func (e *memberEnv) raw(method, path string, body []byte, headers map[string]string) resp {
	e.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.7:51000"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: b, headers: res.Header, cookies: res.Cookies()}
}

func (e *memberEnv) must(r resp, want int, what string) resp {
	e.t.Helper()
	if r.status != want {
		e.t.Fatalf("%s: %d %s", what, r.status, r.body)
	}
	return r
}

func idemHeaders(token string) map[string]string {
	h := bearer(token)
	h["Idempotency-Key"] = uuid.NewString()
	return h
}

// owner registers a dashboard owner and returns the access token.
func (e *memberEnv) owner(email string) string {
	e.t.Helper()
	r := e.must(e.do(http.MethodPost, "/v1/dashboard/auth/register", map[string]any{
		"email": email, "password": "correct horse battery", "tenant_name": "Cafe " + email, "tenant_type": "brand",
	}, nil), http.StatusCreated, "register")
	var sess api.Session
	r.into(e.t, &sess)
	return sess.AccessToken
}

func (e *memberEnv) tenantID(access string) string {
	e.t.Helper()
	var me api.Me
	e.must(e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(access)), http.StatusOK, "me").into(e.t, &me)
	return me.Memberships[0].Tenant.Id.String()
}

// app creates an app with a developing channel so members can sign in.
func (e *memberEnv) app(access string) api.App {
	e.t.Helper()
	var app api.App
	e.must(e.do(http.MethodPost, "/v1/dashboard/tenants/"+e.tenantID(access)+"/apps", map[string]any{"name": "Loyalty"}, idemHeaders(access)), http.StatusCreated, "create app").into(e.t, &app)
	e.seq++
	channel := fmt.Sprintf("%s%02d", memberChannelID[:8], e.seq)
	e.must(e.do(http.MethodPut, "/v1/dashboard/apps/"+app.Id.String()+"/channels/developing",
		map[string]any{"channel_id": channel, "liff_id": channel + "-abcdefgh", "channel_secret": "channel-secret"}, idemHeaders(access)), http.StatusOK, "put channel")
	e.channels[app.Id.String()] = channel
	// The staff app signs in through its own LINE Login channel.
	staffChannel := fmt.Sprintf("%s%02d", "8"+memberChannelID[1:8], e.seq)
	e.must(e.do(http.MethodPut, "/v1/dashboard/apps/"+app.Id.String()+"/channels/staff",
		map[string]any{"channel_id": staffChannel, "liff_id": staffChannel + "-staffapp", "channel_secret": "channel-secret"}, idemHeaders(access)), http.StatusOK, "put staff channel")
	e.staffChannels[app.Id.String()] = staffChannel
	return app
}

// staffChannelOf returns the staff app channel ID registered for the app.
func (e *memberEnv) staffChannelOf(app api.App) string {
	e.t.Helper()
	ch, ok := e.staffChannels[app.Id.String()]
	if !ok {
		e.t.Fatal("no staff channel for app")
	}
	return ch
}

// channelOf returns the channel ID registered for the app.
func (e *memberEnv) channelOf(app api.App) string {
	e.t.Helper()
	ch, ok := e.channels[app.Id.String()]
	if !ok {
		e.t.Fatal("no channel for app")
	}
	return ch
}

// lineLogin signs a LINE user in to the app and returns the session.
func (e *memberEnv) lineLogin(app api.App, sub, name string) api.MemberSession {
	e.t.Helper()
	token := "idtoken-" + sub + "-" + uuid.NewString()
	e.verifier.Add(token, lineauth.Claims{Sub: sub, Name: name, Picture: "https://profile.line-scdn.net/" + sub, Aud: e.channelOf(app)})
	var sess api.MemberSession
	e.must(e.do(http.MethodPost, "/v1/auth/line", map[string]any{"app_id": app.Id, "id_token": token, "language": "zh-TW"}, nil), http.StatusOK, "line login").into(e.t, &sess)
	return sess
}

func TestLineLoginAndMemberSession(t *testing.T) {
	e := newMemberEnv(t)
	ownerTok := e.owner("owner@example.com")
	app := e.app(ownerTok)

	sess := e.lineLogin(app, "U1111", "Mei")
	if !sess.IsNew || sess.Member.AppId != app.Id || *sess.Member.DisplayName != "Mei" || sess.App.Id != app.Id || sess.TokenType != api.MemberSessionTokenTypeBearer {
		t.Fatalf("session: %+v", sess)
	}
	again := e.lineLogin(app, "U1111", "Mei Lin")
	if again.IsNew || again.Member.Id != sess.Member.Id || *again.Member.DisplayName != "Mei Lin" {
		t.Errorf("second login: %+v", again)
	}

	var me api.Member
	e.must(e.do(http.MethodGet, "/v1/me", nil, bearer(sess.SessionToken)), http.StatusOK, "me").into(t, &me)
	if me.Id != sess.Member.Id || me.IsFriend {
		t.Errorf("me: %+v", me)
	}

	// Route-based dispatch: a dashboard token is not a member and vice versa.
	if r := e.do(http.MethodGet, "/v1/me", nil, bearer(ownerTok)); r.status != http.StatusUnauthorized {
		t.Errorf("dashboard token on /v1/me: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(sess.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("member token on /v1/dashboard/me: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/v1/me", nil, nil); r.status != http.StatusUnauthorized {
		t.Errorf("anonymous /v1/me: %d", r.status)
	}

	// Rejections at the HTTP edge.
	if r := e.do(http.MethodPost, "/v1/auth/line", map[string]any{"app_id": app.Id, "id_token": "unknown-token-xxxxxxxxxxxxxxxx"}, nil); r.status != http.StatusUnauthorized {
		t.Errorf("unknown token: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodPost, "/v1/auth/line", map[string]any{"app_id": uuid.New(), "id_token": "unknown-token-xxxxxxxxxxxxxxxx"}, nil); r.status != http.StatusNotFound {
		t.Errorf("unknown app: %d %s", r.status, r.body)
	}
	e.verifier.Add("other-aud-token-xxxxxxxxxxxxxx", lineauth.Claims{Sub: "U9", Aud: "1111111111"})
	if r := e.do(http.MethodPost, "/v1/auth/line", map[string]any{"app_id": app.Id, "id_token": "other-aud-token-xxxxxxxxxxxxxx"}, nil); r.status != http.StatusUnauthorized {
		t.Errorf("foreign audience: %d %s", r.status, r.body)
	}
}

func TestMemberProfileConsentExportAndErasure(t *testing.T) {
	e := newMemberEnv(t)
	ownerTok := e.owner("owner@example.com")
	app := e.app(ownerTok)
	sess := e.lineLogin(app, "U2222", "Chen")
	h := idemHeaders(sess.SessionToken)

	var m api.Member
	e.must(e.do(http.MethodPatch, "/v1/me", map[string]any{"display_name": "Chen W.", "phone": "+886 912 000 111", "birthday": "1990-05-06", "gender": "male", "custom_fields": map[string]any{"vip": true}}, h), http.StatusOK, "update").into(t, &m)
	if *m.DisplayName != "Chen W." || *m.Phone != "+886 912 000 111" || m.Birthday.Format("2006-01-02") != "1990-05-06" || string(*m.Gender) != "male" || m.CustomFields["vip"] != true {
		t.Errorf("updated: %+v", m)
	}
	// Replay with the same key returns the same document; a different body is 422.
	replay := e.must(e.do(http.MethodPatch, "/v1/me", map[string]any{"display_name": "Chen W.", "phone": "+886 912 000 111", "birthday": "1990-05-06", "gender": "male", "custom_fields": map[string]any{"vip": true}}, h), http.StatusOK, "replay")
	var replayed api.Member
	replay.into(t, &replayed)
	if replayed.Id != m.Id || *replayed.DisplayName != "Chen W." {
		t.Errorf("replay: %+v", replayed)
	}
	if r := e.do(http.MethodPatch, "/v1/me", map[string]any{"display_name": "Someone Else"}, h); r.status != http.StatusUnprocessableEntity {
		t.Errorf("mismatch: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodPatch, "/v1/me", map[string]any{"phone": "letters"}, idemHeaders(sess.SessionToken)); r.status != http.StatusBadRequest {
		t.Errorf("validation: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodPatch, "/v1/me", map[string]any{"display_name": "x"}, bearer(sess.SessionToken)); r.status != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key: %d %s", r.status, r.body)
	}

	// Consent twice keeps both rows and reports the latest.
	for _, v := range []string{"2026-10", "2026-11"} {
		e.must(e.do(http.MethodPost, "/v1/me/consent", map[string]any{"version": v}, idemHeaders(sess.SessionToken)), http.StatusOK, "consent "+v).into(t, &m)
	}
	if m.Consent == nil || m.Consent.Version != "2026-11" {
		t.Errorf("consent: %+v", m.Consent)
	}
	var export api.MemberExport
	e.must(e.do(http.MethodGet, "/v1/me/export", nil, bearer(sess.SessionToken)), http.StatusOK, "export").into(t, &export)
	if len(export.Consents) != 2 || deref(export.Member.LineUserId) != "U2222" || export.Member.Id != m.Id {
		t.Errorf("export: %+v", export)
	}

	// Erasure blanks the row, ends the session and lets the LINE user start over.
	e.must(e.do(http.MethodDelete, "/v1/me", nil, bearer(sess.SessionToken)), http.StatusNoContent, "erase")
	if r := e.do(http.MethodGet, "/v1/me", nil, bearer(sess.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("session after erasure: %d %s", r.status, r.body)
	}
	var name, line *string
	var anon bool
	if err := e.db.Admin.QueryRow(context.Background(), "SELECT display_name, line_user_id, anonymized_at IS NOT NULL FROM members WHERE id = $1", uuid.UUID(m.Id)).Scan(&name, &line, &anon); err != nil {
		t.Fatal(err)
	}
	if name != nil || !anon || !strings.HasPrefix(*line, "erased:") {
		t.Errorf("row after erasure: name=%v line=%v anon=%v", name, *line, anon)
	}
	fresh := e.lineLogin(app, "U2222", "Chen")
	if !fresh.IsNew || fresh.Member.Id == m.Id {
		t.Errorf("re-login after erasure: new=%v same=%v", fresh.IsNew, fresh.Member.Id == m.Id)
	}
}

func TestDashboardMemberListsAndDataRights(t *testing.T) {
	e := newMemberEnv(t)
	ownerTok := e.owner("owner@example.com")
	app := e.app(ownerTok)
	var ids []string
	for i := 0; i < 5; i++ {
		e.clk.Advance(time.Second)
		s := e.lineLogin(app, fmt.Sprintf("U%04d", i), fmt.Sprintf("Member %d", i))
		ids = append(ids, s.Member.Id.String())
	}

	// Three pages of two, newest first.
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		path := "/v1/dashboard/apps/" + app.Id.String() + "/members?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var out api.MemberPage
		e.must(e.do(http.MethodGet, path, nil, bearer(ownerTok)), http.StatusOK, "list").into(t, &out)
		for _, m := range out.Items {
			if seen[m.Id.String()] {
				t.Errorf("member %s listed twice", m.Id)
			}
			seen[m.Id.String()] = true
		}
		if page < 2 && (len(out.Items) != 2 || out.NextCursor == nil) {
			t.Fatalf("page %d: %d items, cursor %v", page, len(out.Items), out.NextCursor)
		}
		if page == 2 && (len(out.Items) != 1 || out.NextCursor != nil) {
			t.Fatalf("last page: %d items, cursor %v", len(out.Items), out.NextCursor)
		}
		if out.NextCursor != nil {
			cursor = *out.NextCursor
		}
	}
	if len(seen) != 5 {
		t.Errorf("saw %d members, want 5", len(seen))
	}
	var search api.MemberPage
	e.must(e.do(http.MethodGet, "/v1/dashboard/apps/"+app.Id.String()+"/members?q=member%203", nil, bearer(ownerTok)), http.StatusOK, "search").into(t, &search)
	if len(search.Items) != 1 || *search.Items[0].DisplayName != "Member 3" {
		t.Errorf("search: %+v", search.Items)
	}
	e.must(e.do(http.MethodGet, "/v1/dashboard/apps/"+app.Id.String()+"/members?q=U0004", nil, bearer(ownerTok)), http.StatusOK, "search by line id").into(t, &search)
	if len(search.Items) != 1 || deref(search.Items[0].LineUserId) != "U0004" {
		t.Errorf("search by line id: %+v", search.Items)
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/apps/"+app.Id.String()+"/members?cursor=!!!", nil, bearer(ownerTok)); r.status != http.StatusBadRequest {
		t.Errorf("bad cursor: %d", r.status)
	}

	var detail api.MemberDetail
	e.must(e.do(http.MethodGet, "/v1/dashboard/members/"+ids[0], nil, bearer(ownerTok)), http.StatusOK, "get").into(t, &detail)
	if deref(detail.LineUserId) != "U0000" || detail.LastLoginAt == nil {
		t.Errorf("detail: %+v", detail)
	}

	// Another tenant's owner and an unrelated member see nothing.
	outsider := e.owner("outsider@example.com")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/dashboard/apps/" + app.Id.String() + "/members"},
		{http.MethodGet, "/v1/dashboard/members/" + ids[0]},
		{http.MethodGet, "/v1/dashboard/members/" + ids[0] + "/export"},
		{http.MethodDelete, "/v1/dashboard/members/" + ids[0]},
	} {
		if r := e.do(c.method, c.path, nil, bearer(outsider)); r.status != http.StatusNotFound {
			t.Errorf("outsider %s %s: %d %s", c.method, c.path, r.status, r.body)
		}
	}

	// A staff-role user may read but not erase.
	staff := e.owner("staff@example.com")
	var staffMe api.Me
	e.must(e.do(http.MethodGet, "/v1/dashboard/me", nil, bearer(staff)), http.StatusOK, "staff me").into(t, &staffMe)
	e.db.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'staff')", e.tenantID(ownerTok), uuid.UUID(staffMe.User.Id))
	e.must(e.do(http.MethodGet, "/v1/dashboard/members/"+ids[0], nil, bearer(staff)), http.StatusOK, "staff get")
	if r := e.do(http.MethodDelete, "/v1/dashboard/members/"+ids[0], nil, bearer(staff)); r.status != http.StatusForbidden {
		t.Errorf("staff erase: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/v1/dashboard/members/"+ids[0]+"/export", nil, bearer(staff)); r.status != http.StatusForbidden {
		t.Errorf("staff export: %d %s", r.status, r.body)
	}

	// The owner exports and erases, and both land in the audit log.
	var export api.MemberExport
	e.must(e.do(http.MethodGet, "/v1/dashboard/members/"+ids[0]+"/export", nil, bearer(ownerTok)), http.StatusOK, "owner export").into(t, &export)
	e.must(e.do(http.MethodDelete, "/v1/dashboard/members/"+ids[0], nil, bearer(ownerTok)), http.StatusNoContent, "owner erase")
	if r := e.do(http.MethodGet, "/v1/dashboard/members/"+ids[0], nil, bearer(ownerTok)); r.status != http.StatusNotFound {
		t.Errorf("erased member still visible: %d", r.status)
	}
	var actions []string
	rows, err := e.db.Admin.Query(context.Background(), "SELECT action FROM audit_log WHERE target_id = $1 ORDER BY id", ids[0])
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	rows.Close()
	if strings.Join(actions, ",") != "member.export,member.erase" {
		t.Errorf("audit actions = %v", actions)
	}
}

func TestServerMemberEndpointsNeedTheScope(t *testing.T) {
	e := newMemberEnv(t)
	ownerTok := e.owner("owner@example.com")
	app := e.app(ownerTok)
	sess := e.lineLogin(app, "U7777", "Lin")

	mint := func(scopes []string) string {
		var k api.ApiKeyCreated
		e.must(e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/api-keys", map[string]any{"name": "pos", "scopes": scopes}, idemHeaders(ownerTok)), http.StatusCreated, "api key").into(t, &k)
		return k.Key
	}
	reader := map[string]string{"X-API-Key": mint([]string{"members:read"})}
	writer := map[string]string{"X-API-Key": mint([]string{"points:write"})}

	var page api.MemberPage
	e.must(e.do(http.MethodGet, "/v1/members", nil, reader), http.StatusOK, "server list").into(t, &page)
	if len(page.Items) != 1 || deref(page.Items[0].LineUserId) != "U7777" {
		t.Errorf("server list: %+v", page.Items)
	}
	e.must(e.do(http.MethodGet, "/v1/members/"+sess.Member.Id.String(), nil, reader), http.StatusOK, "server get")
	if r := e.do(http.MethodGet, "/v1/members", nil, writer); r.status != http.StatusForbidden {
		t.Errorf("key without scope: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/v1/members/"+sess.Member.Id.String(), nil, writer); r.status != http.StatusForbidden {
		t.Errorf("key without scope (get): %d %s", r.status, r.body)
	}
	// A member of another app is invisible to this key.
	otherOwner := e.owner("other@example.com")
	otherApp := e.app(otherOwner)
	other := e.lineLogin(otherApp, "U8888", "Other")
	if r := e.do(http.MethodGet, "/v1/members/"+other.Member.Id.String(), nil, reader); r.status != http.StatusNotFound {
		t.Errorf("foreign member via api key: %d %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/v1/members", nil, bearer(sess.SessionToken)); r.status != http.StatusUnauthorized {
		t.Errorf("member token on server route: %d", r.status)
	}
}

func TestMemberFlowsLogNoCredentials(t *testing.T) {
	e := newMemberEnv(t)
	ownerTok := e.owner("owner@example.com")
	app := e.app(ownerTok)
	token := "idtoken-secret-" + uuid.NewString()
	e.verifier.Add(token, lineauth.Claims{Sub: "Uhygiene", Name: "H", Aud: e.channelOf(app)})
	var sess api.MemberSession
	e.must(e.do(http.MethodPost, "/v1/auth/line", map[string]any{"app_id": app.Id, "id_token": token}, nil), http.StatusOK, "login").into(t, &sess)
	e.must(e.do(http.MethodGet, "/v1/me", nil, bearer(sess.SessionToken)), http.StatusOK, "me")
	e.do(http.MethodPost, "/v1/auth/line", map[string]any{"app_id": app.Id, "id_token": "bad-token-" + uuid.NewString()}, nil)

	for _, line := range strings.Split(e.logs.String(), "\n") {
		if line == "" || strings.Contains(line, `"level":"DEBUG"`) {
			continue
		}
		for _, secret := range []string{token, sess.SessionToken, "Uhygiene"} {
			if strings.Contains(line, secret) {
				t.Errorf("log line leaks a credential or LINE user ID: %s", line)
			}
		}
	}
}
