package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// fakeDashboard maps bearer tokens to user principals so these tests do not
// depend on the auth service.
type fakeDashboard struct{ tokens map[string]uuid.UUID }

func (f fakeDashboard) AuthenticateDashboard(_ context.Context, token string) (principal.Principal, error) {
	id, ok := f.tokens[token]
	if !ok {
		return principal.Principal{}, apierr.Unauthorized("bad token")
	}
	return principal.Principal{Kind: principal.KindUser, UserID: id}, nil
}

type world struct {
	t       *testing.T
	db      *testutil.DB
	svc     *tenant.Service
	handler http.Handler

	tenantA, tenantB      uuid.UUID
	ownerA, adminA        uuid.UUID
	staffA, ownerB, loner uuid.UUID
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := testutil.NewDB(t)
	svc := tenant.New(d.App, crypto.Static(bytes.Repeat([]byte{5}, 32)), clock.Real{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := &world{t: t, db: d, svc: svc}
	w.tenantA = d.NewTenant(t, "Alpha")
	w.tenantB = d.NewTenant(t, "Beta")
	w.ownerA = w.user("owner-a@example.com")
	w.adminA = w.user("admin-a@example.com")
	w.staffA = w.user("staff-a@example.com")
	w.ownerB = w.user("owner-b@example.com")
	w.loner = w.user("loner@example.com")
	w.member(w.tenantA, w.ownerA, "owner")
	w.member(w.tenantA, w.adminA, "admin")
	w.member(w.tenantA, w.staffA, "staff")
	w.member(w.tenantB, w.ownerB, "owner")

	srv := New(Deps{
		Config:    &config.Config{Env: config.Development},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:        d.Admin,
		Version:   "test",
		Dashboard: fakeDashboard{tokens: map[string]uuid.UUID{"owner-a": w.ownerA, "admin-a": w.adminA, "staff-a": w.staffA, "owner-b": w.ownerB, "loner": w.loner}},
		APIKeys:   svc,
		Tenants:   svc,
	})
	w.handler = srv.Handler()
	return w
}

func (w *world) user(email string) uuid.UUID {
	id := testutil.NewID()
	w.db.Exec(w.t, "INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'x', $3)", id, email, strings.Split(email, "@")[0])
	return id
}

func (w *world) member(tenantID, userID uuid.UUID, role string) {
	w.db.Exec(w.t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, userID, role)
}

type tresp struct {
	code int
	body []byte
}

func (r tresp) into(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r tresp) errCode(t *testing.T) string {
	t.Helper()
	var e api.Error
	r.into(t, &e)
	return e.Code
}

// call sends a request. token selects the fake dashboard user; apiKey sets
// X-API-Key; idemKey sets Idempotency-Key when non-empty.
func (w *world) call(method, path, token, apiKey, idemKey string, body any) tresp {
	w.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			w.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return tresp{code: rec.Code, body: rec.Body.Bytes()}
}

func (w *world) as(token string) *caller { return &caller{w: w, token: token} }

type caller struct {
	w     *world
	token string
}

func (c *caller) get(path string) tresp { return c.w.call(http.MethodGet, path, c.token, "", "", nil) }
func (c *caller) del(path string) tresp {
	return c.w.call(http.MethodDelete, path, c.token, "", "", nil)
}
func (c *caller) post(path, key string, body any) tresp {
	return c.w.call(http.MethodPost, path, c.token, "", key, body)
}
func (c *caller) patch(path, key string, body any) tresp {
	return c.w.call(http.MethodPatch, path, c.token, "", key, body)
}
func (c *caller) put(path, key string, body any) tresp {
	return c.w.call(http.MethodPut, path, c.token, "", key, body)
}

func (w *world) createApp(token string, tenantID uuid.UUID, name string) api.App {
	w.t.Helper()
	r := w.as(token).post("/v1/dashboard/tenants/"+tenantID.String()+"/apps", uuid.NewString(), map[string]any{"name": name})
	if r.code != http.StatusCreated {
		w.t.Fatalf("create app: %d %s", r.code, r.body)
	}
	var app api.App
	r.into(w.t, &app)
	return app
}

func want(t *testing.T, r tresp, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status = %d, want %d; body %s", r.code, code, r.body)
	}
}

func TestAppsCreateListGetAndIdempotency(t *testing.T) {
	w := newWorld(t)
	owner := w.as("owner-a")
	base := "/v1/dashboard/tenants/" + w.tenantA.String() + "/apps"

	key := uuid.NewString()
	first := owner.post(base, key, map[string]any{"name": "Cafe Loyalty", "locale": "zh-TW"})
	want(t, first, http.StatusCreated)
	var app api.App
	first.into(t, &app)
	if app.Slug != "cafe-loyalty" || app.TenantId != w.tenantA || app.OaChannelSecretSet {
		t.Errorf("unexpected app: %+v", app)
	}

	replay := owner.post(base, key, map[string]any{"name": "Cafe Loyalty", "locale": "zh-TW"})
	want(t, replay, http.StatusCreated)
	if !bytes.Equal(first.body, replay.body) {
		t.Errorf("replay body differs:\n%s\n%s", first.body, replay.body)
	}
	mismatch := owner.post(base, key, map[string]any{"name": "Other"})
	want(t, mismatch, http.StatusUnprocessableEntity)
	if mismatch.errCode(t) != "idempotency_mismatch" {
		t.Errorf("code = %s", mismatch.errCode(t))
	}
	noKey := w.call(http.MethodPost, base, "owner-a", "", "", map[string]any{"name": "No key"})
	want(t, noKey, http.StatusBadRequest)

	list := owner.get(base)
	want(t, list, http.StatusOK)
	var apps []api.App
	list.into(t, &apps)
	if len(apps) != 1 {
		t.Fatalf("apps = %d (the replay must not create a second app)", len(apps))
	}
	got := owner.get("/v1/dashboard/apps/" + app.Id.String())
	want(t, got, http.StatusOK)

	// Free plan: one app only.
	second := owner.post(base, uuid.NewString(), map[string]any{"name": "Second"})
	want(t, second, http.StatusConflict)

	// Validation.
	bad := owner.post(base, uuid.NewString(), map[string]any{"name": "   "})
	want(t, bad, http.StatusBadRequest)
	badSlug := owner.post(base, uuid.NewString(), map[string]any{"name": "X", "slug": "Bad Slug"})
	want(t, badSlug, http.StatusBadRequest)
}

func TestCrossTenantAccessIsNotFound(t *testing.T) {
	w := newWorld(t)
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	appPath := "/v1/dashboard/apps/" + app.Id.String()
	store := w.as("owner-a").post(appPath+"/stores", uuid.NewString(), map[string]any{"name": "Main"})
	want(t, store, http.StatusCreated)
	var st api.Store
	store.into(t, &st)
	keyResp := w.as("owner-a").post(appPath+"/api-keys", uuid.NewString(), map[string]any{"name": "pos", "scopes": []string{"members:read"}})
	want(t, keyResp, http.StatusCreated)
	var created api.ApiKeyCreated
	keyResp.into(t, &created)

	for _, outsider := range []string{"owner-b", "loner"} {
		c := w.as(outsider)
		cases := []struct {
			name string
			r    tresp
		}{
			{"get tenant", c.get("/v1/dashboard/tenants/" + w.tenantA.String())},
			{"list team", c.get("/v1/dashboard/tenants/" + w.tenantA.String() + "/team")},
			{"list apps", c.get("/v1/dashboard/tenants/" + w.tenantA.String() + "/apps")},
			{"audit", c.get("/v1/dashboard/tenants/" + w.tenantA.String() + "/audit-log")},
			{"get app", c.get(appPath)},
			{"patch app", c.patch(appPath, uuid.NewString(), map[string]any{"name": "Hijack"})},
			{"channels", c.get(appPath + "/channels")},
			{"put channel", c.put(appPath+"/channels/developing", uuid.NewString(), map[string]any{"channel_id": "1", "liff_id": "2", "channel_secret": "s"})},
			{"stores", c.get(appPath + "/stores")},
			{"patch store", c.patch("/v1/dashboard/stores/"+st.Id.String(), uuid.NewString(), map[string]any{"name": "x"})},
			{"delete store", c.del("/v1/dashboard/stores/" + st.Id.String())},
			{"staff", c.get(appPath + "/staff")},
			{"api keys", c.get(appPath + "/api-keys")},
			{"revoke key", c.del("/v1/dashboard/api-keys/" + created.Id.String())},
			{"rename tenant", c.patch("/v1/dashboard/tenants/"+w.tenantA.String(), uuid.NewString(), map[string]any{"name": "Mine"})},
		}
		for _, tc := range cases {
			if tc.r.code != http.StatusNotFound {
				t.Errorf("%s as %s: status = %d, want 404; body %s", tc.name, outsider, tc.r.code, tc.r.body)
			}
		}
	}
	// Nothing leaked or changed.
	again := w.as("owner-a").get(appPath)
	var same api.App
	again.into(t, &same)
	if same.Name != "Cafe" {
		t.Errorf("app was modified by an outsider: %+v", same)
	}
}

func TestStaffRoleIsReadOnly(t *testing.T) {
	w := newWorld(t)
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	staff := w.as("staff-a")
	want(t, staff.get("/v1/dashboard/tenants/"+w.tenantA.String()+"/apps"), http.StatusOK)
	want(t, staff.get("/v1/dashboard/apps/"+app.Id.String()), http.StatusOK)
	want(t, staff.patch("/v1/dashboard/apps/"+app.Id.String(), uuid.NewString(), map[string]any{"name": "No"}), http.StatusForbidden)
	want(t, staff.post("/v1/dashboard/apps/"+app.Id.String()+"/stores", uuid.NewString(), map[string]any{"name": "No"}), http.StatusForbidden)
	want(t, staff.patch("/v1/dashboard/tenants/"+w.tenantA.String(), uuid.NewString(), map[string]any{"name": "No"}), http.StatusForbidden)
	want(t, staff.post("/v1/dashboard/tenants/"+w.tenantA.String()+"/invitations", uuid.NewString(), map[string]any{"email": "loner@example.com", "role": "staff"}), http.StatusForbidden)
	// Unauthenticated and unknown tokens.
	want(t, w.call(http.MethodGet, "/v1/dashboard/tenants", "", "", "", nil), http.StatusUnauthorized)
	want(t, w.call(http.MethodGet, "/v1/dashboard/tenants", "nobody", "", "", nil), http.StatusUnauthorized)
}

func TestTenantsAndMemberships(t *testing.T) {
	w := newWorld(t)
	r := w.as("owner-a").get("/v1/dashboard/tenants")
	want(t, r, http.StatusOK)
	var ms []api.Membership
	r.into(t, &ms)
	if len(ms) != 1 || ms[0].Tenant.Id != w.tenantA || ms[0].Role != "owner" {
		t.Fatalf("memberships: %+v", ms)
	}
	renamed := w.as("admin-a").patch("/v1/dashboard/tenants/"+w.tenantA.String(), uuid.NewString(), map[string]any{"name": "Alpha Brands"})
	want(t, renamed, http.StatusOK)
	var tn api.Tenant
	renamed.into(t, &tn)
	if tn.Name != "Alpha Brands" {
		t.Errorf("name = %s", tn.Name)
	}
}

func TestTeamRules(t *testing.T) {
	w := newWorld(t)
	team := "/v1/dashboard/tenants/" + w.tenantA.String() + "/team"
	owner, admin := w.as("owner-a"), w.as("admin-a")

	// People join through invitations (invitation_handlers_test.go); here the
	// loner is already a staff member.
	w.member(w.tenantA, w.loner, "staff")

	// Role updates are owner-only; the last owner cannot be demoted or removed.
	want(t, admin.patch(team+"/"+w.loner.String(), uuid.NewString(), map[string]any{"role": "admin"}), http.StatusForbidden)
	want(t, owner.patch(team+"/"+w.ownerA.String(), uuid.NewString(), map[string]any{"role": "admin"}), http.StatusConflict)
	want(t, owner.del(team+"/"+w.ownerA.String()), http.StatusConflict)
	want(t, admin.del(team+"/"+w.ownerA.String()), http.StatusForbidden)

	// A second owner frees the first.
	want(t, owner.patch(team+"/"+w.adminA.String(), uuid.NewString(), map[string]any{"role": "owner"}), http.StatusOK)
	want(t, owner.patch(team+"/"+w.ownerA.String(), uuid.NewString(), map[string]any{"role": "admin"}), http.StatusOK)
	want(t, w.as("admin-a").del(team+"/"+w.loner.String()), http.StatusNoContent)
	want(t, w.as("admin-a").del(team+"/"+w.loner.String()), http.StatusNotFound)

	list := owner.get(team)
	want(t, list, http.StatusOK)
	var members []api.TeamMember
	list.into(t, &members)
	if len(members) != 3 {
		t.Errorf("team size = %d, want 3", len(members))
	}
}

func TestAPIKeysLifecycle(t *testing.T) {
	w := newWorld(t)
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	keys := "/v1/dashboard/apps/" + app.Id.String() + "/api-keys"
	owner := w.as("owner-a")

	want(t, owner.post(keys, uuid.NewString(), map[string]any{"name": "pos", "scopes": []string{}}), http.StatusBadRequest)
	want(t, owner.post(keys, uuid.NewString(), map[string]any{"name": "pos", "scopes": []string{"root:all"}}), http.StatusBadRequest)

	createKey := uuid.NewString()
	created := owner.post(keys, createKey, map[string]any{"name": "POS", "scopes": []string{"members:read", "points:write"}})
	want(t, created, http.StatusCreated)
	var k api.ApiKeyCreated
	created.into(t, &k)
	if !strings.HasPrefix(k.Key, "ml_") || len(k.Key) < 40 || k.Prefix != k.Key[:11] || len(k.Scopes) != 2 {
		t.Fatalf("key: %+v", k)
	}
	replay := owner.post(keys, createKey, map[string]any{"name": "POS", "scopes": []string{"members:read", "points:write"}})
	want(t, replay, http.StatusCreated)
	var again api.ApiKeyCreated
	replay.into(t, &again)
	if again.Key != k.Key {
		t.Error("replay must return the same key")
	}

	list := owner.get(keys)
	want(t, list, http.StatusOK)
	if bytes.Contains(list.body, []byte(k.Key)) {
		t.Error("list must never include the full key")
	}

	me := w.call(http.MethodGet, "/v1/app", "", k.Key, "", nil)
	want(t, me, http.StatusOK)
	var current api.App
	me.into(t, &current)
	if current.Id != app.Id {
		t.Errorf("current app = %s, want %s", current.Id, app.Id)
	}
	want(t, w.call(http.MethodGet, "/v1/app", "", "ml_not-a-real-key-at-all-xxxxxxxxxxxxxxxxxxxxx", "", nil), http.StatusUnauthorized)
	want(t, w.call(http.MethodGet, "/v1/app", "", "garbage", "", nil), http.StatusUnauthorized)
	// Dashboard bearer tokens cannot use the server API.
	want(t, owner.get("/v1/app"), http.StatusUnauthorized)

	// Disabled app: key stops working.
	want(t, owner.patch("/v1/dashboard/apps/"+app.Id.String(), uuid.NewString(), map[string]any{"status": "disabled"}), http.StatusOK)
	want(t, w.call(http.MethodGet, "/v1/app", "", k.Key, "", nil), http.StatusUnauthorized)
	want(t, owner.patch("/v1/dashboard/apps/"+app.Id.String(), uuid.NewString(), map[string]any{"status": "active"}), http.StatusOK)
	want(t, w.call(http.MethodGet, "/v1/app", "", k.Key, "", nil), http.StatusOK)

	want(t, owner.del("/v1/dashboard/api-keys/"+k.Id.String()), http.StatusNoContent)
	want(t, w.call(http.MethodGet, "/v1/app", "", k.Key, "", nil), http.StatusUnauthorized)
	want(t, owner.del("/v1/dashboard/api-keys/"+k.Id.String()), http.StatusNotFound)
}

func TestOASecretRoundTripsThroughTheKeyring(t *testing.T) {
	w := newWorld(t)
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	path := "/v1/dashboard/apps/" + app.Id.String()
	owner := w.as("owner-a")

	r := owner.patch(path, uuid.NewString(), map[string]any{"oa_channel_id": "1654000000", "oa_basic_id": "@cafe", "oa_channel_secret": "super-secret-value"})
	want(t, r, http.StatusOK)
	var updated api.App
	r.into(t, &updated)
	if !updated.OaChannelSecretSet || updated.OaChannelId == nil || *updated.OaChannelId != "1654000000" {
		t.Fatalf("app: %+v", updated)
	}
	if bytes.Contains(r.body, []byte("super-secret-value")) {
		t.Fatal("secret leaked into the response")
	}

	row, err := w.svc.GetApp(context.Background(), principal.Principal{Kind: principal.KindUser, UserID: w.ownerA}, uuid.UUID(app.Id))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.OaChannelSecretEnc, []byte("super-secret-value")) {
		t.Fatal("secret stored in plaintext")
	}
	secret, ok, err := w.svc.OAChannelSecret(context.Background(), row)
	if err != nil || !ok || secret != "super-secret-value" {
		t.Fatalf("decrypt: %q %v %v", secret, ok, err)
	}

	// Clearing with an empty string.
	r = owner.patch(path, uuid.NewString(), map[string]any{"oa_channel_secret": ""})
	want(t, r, http.StatusOK)
	r.into(t, &updated)
	if updated.OaChannelSecretSet {
		t.Error("secret should be cleared")
	}

	// A second app cannot claim the same OA channel.
	w.db.Exec(t, "UPDATE tenants SET plan = 'agency' WHERE id = $1", w.tenantA)
	other := w.createApp("owner-a", w.tenantA, "Second")
	want(t, owner.patch("/v1/dashboard/apps/"+other.Id.String(), uuid.NewString(), map[string]any{"oa_channel_id": "1654000000"}), http.StatusConflict)
}

func TestChannels(t *testing.T) {
	w := newWorld(t)
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	base := "/v1/dashboard/apps/" + app.Id.String() + "/channels"
	owner := w.as("owner-a")

	want(t, owner.put(base+"/production", uuid.NewString(), map[string]any{"channel_id": "100", "liff_id": "100-abc", "channel_secret": "s"}), http.StatusBadRequest)
	put := owner.put(base+"/developing", uuid.NewString(), map[string]any{"channel_id": "2007000001", "liff_id": "2007000001-AbCdEf", "channel_secret": "s3cret"})
	want(t, put, http.StatusOK)
	var ch api.AppChannel
	put.into(t, &ch)
	if ch.Environment != "developing" || !ch.ChannelSecretSet {
		t.Fatalf("channel: %+v", ch)
	}
	// The secret is required: it proves control of the channel.
	want(t, owner.put(base+"/developing", uuid.NewString(), map[string]any{"channel_id": "2007000001", "liff_id": "2007000001-AbCdEf"}), http.StatusBadRequest)
	put = owner.put(base+"/developing", uuid.NewString(), map[string]any{"channel_id": "2007000001", "liff_id": "2007000001-AbCdEf", "channel_secret": "s3cret"})
	want(t, put, http.StatusOK)
	put.into(t, &ch)
	if !ch.ChannelSecretSet || ch.VerifiedAt != nil {
		t.Errorf("channel without a verifier: %+v", ch)
	}
	list := owner.get(base)
	want(t, list, http.StatusOK)
	var chans []api.AppChannel
	list.into(t, &chans)
	if len(chans) != 1 {
		t.Fatalf("channels = %d", len(chans))
	}

	// LIFF IDs are globally unique.
	want(t, owner.put(base+"/review", uuid.NewString(), map[string]any{"channel_id": "2007000001", "liff_id": "2007000001-AbCdEf", "channel_secret": "s3cret"}), http.StatusConflict)

	// The stored secret decrypts.
	rows, err := w.svc.ListChannels(context.Background(), principal.Principal{Kind: principal.KindUser, UserID: w.ownerA}, uuid.UUID(app.Id))
	if err != nil || len(rows) != 1 {
		t.Fatal(err, rows)
	}
	if secret, ok, err := w.svc.ChannelSecret(context.Background(), rows[0]); err != nil || !ok || secret != "s3cret" {
		t.Fatalf("channel secret: %q %v %v", secret, ok, err)
	}

	want(t, owner.del(base+"/developing"), http.StatusNoContent)
	want(t, owner.del(base+"/developing"), http.StatusNotFound)
}

func TestStoresAndStaff(t *testing.T) {
	w := newWorld(t)
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	appPath := "/v1/dashboard/apps/" + app.Id.String()
	owner := w.as("owner-a")

	s1 := owner.post(appPath+"/stores", uuid.NewString(), map[string]any{"name": "Main", "code": "TPE01", "address": "Taipei"})
	want(t, s1, http.StatusCreated)
	var store api.Store
	s1.into(t, &store)
	want(t, owner.post(appPath+"/stores", uuid.NewString(), map[string]any{"name": "Dup", "code": "TPE01"}), http.StatusConflict)

	upd := owner.patch("/v1/dashboard/stores/"+store.Id.String(), uuid.NewString(), map[string]any{"code": "", "status": "disabled"})
	want(t, upd, http.StatusOK)
	var updated api.Store
	upd.into(t, &updated)
	if updated.Code != nil || updated.Status != "disabled" || updated.Address == nil {
		t.Errorf("store after update: %+v", updated)
	}

	// Staff with store, invite code shown once.
	created := owner.post(appPath+"/staff", uuid.NewString(), map[string]any{"display_name": "Mei", "store_id": store.Id, "role": "manager"})
	want(t, created, http.StatusCreated)
	var staff api.StaffCreated
	created.into(t, &staff)
	if len(staff.InviteCode) != 8 || staff.StoreId == nil || *staff.StoreId != store.Id || staff.LineLinked {
		t.Fatalf("staff: %+v", staff)
	}
	list := owner.get(appPath + "/staff")
	want(t, list, http.StatusOK)
	if bytes.Contains(list.body, []byte(staff.InviteCode)) {
		t.Error("invite code must not appear in lists")
	}

	// Store from another app is rejected.
	w.db.Exec(t, "UPDATE tenants SET plan = 'agency' WHERE id = $1", w.tenantA)
	other := w.createApp("owner-a", w.tenantA, "Other")
	otherStore := owner.post("/v1/dashboard/apps/"+other.Id.String()+"/stores", uuid.NewString(), map[string]any{"name": "Elsewhere"})
	want(t, otherStore, http.StatusCreated)
	var os api.Store
	otherStore.into(t, &os)
	want(t, owner.post(appPath+"/staff", uuid.NewString(), map[string]any{"display_name": "Wrong", "store_id": os.Id}), http.StatusNotFound)

	// Store with staff cannot be deleted until staff are gone.
	want(t, owner.del("/v1/dashboard/stores/"+store.Id.String()), http.StatusConflict)
	want(t, owner.patch("/v1/dashboard/staff/"+staff.Id.String(), uuid.NewString(), map[string]any{"role": "cashier"}), http.StatusOK)
	want(t, owner.del("/v1/dashboard/staff/"+staff.Id.String()), http.StatusNoContent)
	want(t, owner.del("/v1/dashboard/staff/"+staff.Id.String()), http.StatusNotFound)
	want(t, owner.del("/v1/dashboard/stores/"+store.Id.String()), http.StatusNoContent)
}

func TestAuditLogRecordsAndPages(t *testing.T) {
	w := newWorld(t)
	owner := w.as("owner-a")
	app := w.createApp("owner-a", w.tenantA, "Cafe")
	appPath := "/v1/dashboard/apps/" + app.Id.String()
	want(t, owner.patch(appPath, uuid.NewString(), map[string]any{"name": "Cafe 2"}), http.StatusOK)
	want(t, owner.post(appPath+"/stores", uuid.NewString(), map[string]any{"name": "Main"}), http.StatusCreated)
	want(t, owner.post(appPath+"/api-keys", uuid.NewString(), map[string]any{"name": "k", "scopes": []string{"members:read"}}), http.StatusCreated)

	base := "/v1/dashboard/tenants/" + w.tenantA.String() + "/audit-log"
	first := owner.get(base + "?limit=2")
	want(t, first, http.StatusOK)
	var page api.AuditPage
	first.into(t, &page)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page 1: %+v", page)
	}
	if page.Items[0].Action != "api_key.create" || page.Items[1].Action != "store.create" {
		t.Errorf("order: %s, %s", page.Items[0].Action, page.Items[1].Action)
	}
	if page.Items[0].ActorId == nil || *page.Items[0].ActorId != w.ownerA || page.Items[0].RequestId == nil {
		t.Errorf("actor or request id missing: %+v", page.Items[0])
	}
	if meta := page.Items[0].Metadata; meta == nil || (*meta)["name"] != "k" {
		t.Errorf("metadata: %v", meta)
	}

	second := owner.get(base + "?limit=2&cursor=" + *page.NextCursor)
	want(t, second, http.StatusOK)
	var page2 api.AuditPage
	second.into(t, &page2)
	if len(page2.Items) != 2 || page2.Items[0].Action != "app.update" || page2.Items[1].Action != "app.create" {
		t.Fatalf("page 2: %+v", page2)
	}
	if page2.Items[0].Id >= page.Items[1].Id {
		t.Error("pages overlap")
	}
	want(t, owner.get(base+"?cursor=***"), http.StatusBadRequest)

	// Filter by app.
	filtered := owner.get(base + "?app_id=" + uuid.NewString())
	want(t, filtered, http.StatusOK)
	var none api.AuditPage
	filtered.into(t, &none)
	if len(none.Items) != 0 {
		t.Errorf("filter leaked %d items", len(none.Items))
	}

	// Entries are visible only through the tenant.
	want(t, w.as("owner-b").get(base), http.StatusNotFound)
	var count int
	if err := w.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE tenant_id = $1", w.tenantA).Scan(&count); err != nil || count != 4 {
		t.Errorf("audit rows = %d, %v", count, err)
	}
}
