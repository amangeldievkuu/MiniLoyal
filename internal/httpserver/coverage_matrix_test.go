package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// The coverage matrix: one entry per operation in api/openapi.yaml, grouped
// by the principal it serves. tenantOps are dashboard operations on a
// tenant's resources; memberOps act on the signed-in MINI App member;
// serverOps are the API-key routes; staffOps are the staff app routes
// (coverage_staff_matrix_test.go). TestEveryOperationIsInTheCoverageTable
// fails when the spec gains an operation that is in none of the tables nor
// otherCoveredOps, so new endpoints cannot ship without a cross-tenant,
// role, idempotency and credential-kind test.

// matrixFixtures are tenant A's resources the table operates on.
type matrixFixtures struct {
	w          *world
	app        api.App
	store      api.Store
	staff      api.StaffCreated
	key        api.ApiKeyCreated
	invitation api.InvitationCreated
	member     api.MemberSession // a LINE member of app A, signed in
	channel    string            // environment of the fixture channel

	// second is another member of app A and annex another app of tenant A:
	// the alternative targets for body-less writes in the idempotency matrix.
	second api.MemberSession
	annex  uuid.UUID
	// cashier is a second staff member of app A. staffSess and cashierSess
	// are their staff app sessions (f.staff is the manager).
	cashier                api.StaffCreated
	staffSess, cashierSess api.StaffSession
	// scan is the scan token staff request bodies carry; tests set it.
	scan string
}

// Fixture channel and LINE user of tenant A's app.
const (
	fixtureChannelID = "2007000001"
	fixtureLineUser  = "Ufixturea0000000000000000000001"
	fixtureLineName  = "Hsiao-Ling"
)

// opCase describes one spec operation.
type opCase struct {
	id     string
	method string
	path   func(f *matrixFixtures) string
	// body is the request body; alt is the same operation with a different
	// payload, used for the idempotency mismatch case. Both nil for GET and
	// DELETE.
	body, alt func(f *matrixFixtures) any
	// idem is true when the spec requires Idempotency-Key.
	idem bool
	// min is the weakest role the operation accepts.
	min principal.Role
	// created is the status a fresh successful write returns.
	created int
	// creates is the table that gains one row on a successful write; upsert
	// marks writes that replace an existing row when run a second time.
	// more lists further tables that gain one row per successful run.
	creates string
	upsert  bool
	more    []string
	// altPath is the same operation on another resource. Body-less writes
	// use it for the idempotency mismatch case: the key is bound to the
	// path, so reusing it elsewhere is a different request.
	altPath func(f *matrixFixtures) string
	// scope is the API key scope a server operation needs.
	scope string
}

// opKey normalises an operationId: oapi-codegen UpperCamelCases the IDs in
// the embedded spec.
func opKey(id string) string {
	if id == "" {
		return id
	}
	return strings.ToLower(id[:1]) + id[1:]
}

func (c opCase) write() bool { return c.method != http.MethodGet }

func tenantPath(f *matrixFixtures) string {
	return "/v1/dashboard/tenants/" + f.w.tenantA.String()
}

func appPath(f *matrixFixtures) string {
	return "/v1/dashboard/apps/" + f.app.Id.String()
}

func obj(kv ...any) func(*matrixFixtures) any {
	return func(*matrixFixtures) any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
}

var tenantOps = []opCase{
	// tenants
	{id: "getTenant", method: http.MethodGet, path: tenantPath, min: principal.RoleStaff},
	{id: "updateTenant", method: http.MethodPatch, path: tenantPath, body: obj("name", "Renamed"), alt: obj("name", "Other"), idem: true, min: principal.RoleAdmin, created: 200},
	{id: "listTeam", method: http.MethodGet, path: func(f *matrixFixtures) string { return tenantPath(f) + "/team" }, min: principal.RoleStaff},
	{id: "listInvitations", method: http.MethodGet, path: func(f *matrixFixtures) string { return tenantPath(f) + "/invitations" }, min: principal.RoleStaff},
	{id: "createInvitation", method: http.MethodPost, path: func(f *matrixFixtures) string { return tenantPath(f) + "/invitations" },
		body: obj("email", "newcomer@example.com", "role", "staff"), alt: obj("email", "newcomer@example.com", "role", "admin"), idem: true, min: principal.RoleAdmin, created: 201, creates: "invitations"},
	{id: "revokeInvitation", method: http.MethodDelete, path: func(f *matrixFixtures) string { return "/v1/dashboard/invitations/" + f.invitation.Id.String() }, min: principal.RoleAdmin},
	{id: "updateTeamMember", method: http.MethodPatch, path: func(f *matrixFixtures) string { return tenantPath(f) + "/team/" + f.w.staffA.String() },
		body: obj("role", "admin"), alt: obj("role", "staff"), idem: true, min: principal.RoleOwner, created: 200},
	{id: "removeTeamMember", method: http.MethodDelete, path: func(f *matrixFixtures) string { return tenantPath(f) + "/team/" + f.w.staffA.String() }, min: principal.RoleAdmin},
	{id: "listAuditLog", method: http.MethodGet, path: func(f *matrixFixtures) string { return tenantPath(f) + "/audit-log" }, min: principal.RoleStaff},
	// apps
	{id: "listApps", method: http.MethodGet, path: func(f *matrixFixtures) string { return tenantPath(f) + "/apps" }, min: principal.RoleStaff},
	{id: "createApp", method: http.MethodPost, path: func(f *matrixFixtures) string { return tenantPath(f) + "/apps" },
		body: obj("name", "New App"), alt: obj("name", "Other App"), idem: true, min: principal.RoleAdmin, created: 201, creates: "apps"},
	{id: "getApp", method: http.MethodGet, path: appPath, min: principal.RoleStaff},
	{id: "updateApp", method: http.MethodPatch, path: appPath, body: obj("name", "Cafe Renamed"), alt: obj("name", "Cafe Other"), idem: true, min: principal.RoleAdmin, created: 200},
	{id: "listAppChannels", method: http.MethodGet, path: func(f *matrixFixtures) string { return appPath(f) + "/channels" }, min: principal.RoleStaff},
	{id: "putAppChannel", method: http.MethodPut, path: func(f *matrixFixtures) string { return appPath(f) + "/channels/review" },
		body: obj("channel_id", "2007000002", "liff_id", "2007000002-rev", "channel_secret", "rev-secret"), alt: obj("channel_id", "2007000002", "liff_id", "2007000002-alt", "channel_secret", "rev-secret"), idem: true, min: principal.RoleAdmin, created: 200, creates: "app_channels", upsert: true},
	{id: "deleteAppChannel", method: http.MethodDelete, path: func(f *matrixFixtures) string { return appPath(f) + "/channels/" + f.channel }, min: principal.RoleAdmin},
	// stores
	{id: "listStores", method: http.MethodGet, path: func(f *matrixFixtures) string { return appPath(f) + "/stores" }, min: principal.RoleStaff},
	{id: "createStore", method: http.MethodPost, path: func(f *matrixFixtures) string { return appPath(f) + "/stores" },
		body: obj("name", "Branch"), alt: obj("name", "Other Branch"), idem: true, min: principal.RoleAdmin, created: 201, creates: "stores"},
	{id: "updateStore", method: http.MethodPatch, path: func(f *matrixFixtures) string { return "/v1/dashboard/stores/" + f.store.Id.String() },
		body: obj("name", "Main Renamed"), alt: obj("name", "Main Other"), idem: true, min: principal.RoleAdmin, created: 200},
	{id: "deleteStore", method: http.MethodDelete, path: func(f *matrixFixtures) string { return "/v1/dashboard/stores/" + f.store.Id.String() }, min: principal.RoleAdmin},
	// staff
	{id: "listStaff", method: http.MethodGet, path: func(f *matrixFixtures) string { return appPath(f) + "/staff" }, min: principal.RoleStaff},
	{id: "createStaff", method: http.MethodPost, path: func(f *matrixFixtures) string { return appPath(f) + "/staff" },
		body: obj("display_name", "New Staff"), alt: obj("display_name", "Other Staff"), idem: true, min: principal.RoleAdmin, created: 201, creates: "staff"},
	{id: "updateStaff", method: http.MethodPatch, path: func(f *matrixFixtures) string { return "/v1/dashboard/staff/" + f.staff.Id.String() },
		body: obj("display_name", "Mei Renamed"), alt: obj("display_name", "Mei Other"), idem: true, min: principal.RoleAdmin, created: 200},
	{id: "deleteStaff", method: http.MethodDelete, path: func(f *matrixFixtures) string { return "/v1/dashboard/staff/" + f.staff.Id.String() }, min: principal.RoleAdmin},
	// api keys
	{id: "listApiKeys", method: http.MethodGet, path: func(f *matrixFixtures) string { return appPath(f) + "/api-keys" }, min: principal.RoleStaff},
	{id: "createApiKey", method: http.MethodPost, path: func(f *matrixFixtures) string { return appPath(f) + "/api-keys" },
		body: obj("name", "k2", "scopes", []string{"members:read"}), alt: obj("name", "k2", "scopes", []string{"members:write"}), idem: true, min: principal.RoleAdmin, created: 201, creates: "api_keys"},
	{id: "revokeApiKey", method: http.MethodDelete, path: func(f *matrixFixtures) string { return "/v1/dashboard/api-keys/" + f.key.Id.String() }, min: principal.RoleAdmin},
	// members (dashboard)
	{id: "listMembers", method: http.MethodGet, path: func(f *matrixFixtures) string { return appPath(f) + "/members" }, min: principal.RoleStaff},
	{id: "getMember", method: http.MethodGet, path: memberPath, min: principal.RoleStaff},
	// Export is a read that staff may not perform: data-rights requests are
	// handled by owners and admins and audited.
	{id: "exportMember", method: http.MethodGet, path: func(f *matrixFixtures) string { return memberPath(f) + "/export" }, min: principal.RoleAdmin},
	{id: "eraseMember", method: http.MethodDelete, path: memberPath, min: principal.RoleAdmin},
	// Member cards and sessions are reset by owners and admins only; neither
	// takes a body, so the mismatch case reuses the key on another target.
	{id: "rotateMemberCard", method: http.MethodPost, path: func(f *matrixFixtures) string { return memberPath(f) + "/card/rotate" },
		altPath: func(f *matrixFixtures) string {
			return "/v1/dashboard/members/" + f.second.Member.Id.String() + "/card/rotate"
		},
		idem: true, min: principal.RoleAdmin, created: 200},
	{id: "endMemberSessions", method: http.MethodPost, path: func(f *matrixFixtures) string { return appPath(f) + "/member-sessions/end" },
		altPath: func(f *matrixFixtures) string {
			return "/v1/dashboard/apps/" + f.annex.String() + "/member-sessions/end"
		},
		idem: true, min: principal.RoleAdmin, created: 200},
	{id: "resetStaffInvite", method: http.MethodPost, path: func(f *matrixFixtures) string { return "/v1/dashboard/staff/" + f.staff.Id.String() + "/invite" },
		altPath: func(f *matrixFixtures) string { return "/v1/dashboard/staff/" + f.cashier.Id.String() + "/invite" },
		idem:    true, min: principal.RoleAdmin, created: 200},
	// points
	{id: "getPointsRules", method: http.MethodGet, path: func(f *matrixFixtures) string { return appPath(f) + "/points-rules" }, min: principal.RoleStaff},
	{id: "putPointsRules", method: http.MethodPut, path: func(f *matrixFixtures) string { return appPath(f) + "/points-rules" },
		body: rulesBody(50), alt: rulesBody(60), idem: true, min: principal.RoleAdmin, created: 200, creates: "points_rules", upsert: true},
	{id: "getMemberPoints", method: http.MethodGet, path: func(f *matrixFixtures) string { return memberPath(f) + "/points" }, min: principal.RoleStaff},
	{id: "listMemberPointsHistory", method: http.MethodGet, path: func(f *matrixFixtures) string { return memberPath(f) + "/points/history" }, min: principal.RoleStaff},
	{id: "adjustMemberPoints", method: http.MethodPost, path: func(f *matrixFixtures) string { return memberPath(f) + "/points/adjustments" },
		body: obj("delta", 5, "note", "goodwill"), alt: obj("delta", 6, "note", "goodwill"), idem: true, min: principal.RoleAdmin, created: 200,
		creates: "points_lots", more: []string{"points_ledger"}},
}

func rulesBody(spendPerPoint int) func(*matrixFixtures) any {
	return obj("currency", "TWD", "spend_per_point", spendPerPoint, "points_per_unit", 1, "min_spend", 0, "expiry_days", 365, "enabled", true)
}

func memberPath(f *matrixFixtures) string {
	return "/v1/dashboard/members/" + f.member.Member.Id.String()
}

// memberOps act on the caller's own member row through a member session.
// The matrix in coverage_member_matrix_test.go proves a member sees only
// their own data and that no other credential kind is accepted.
var memberOps = []opCase{
	{id: "getMe", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/me" }},
	{id: "updateMe", method: http.MethodPatch, path: func(*matrixFixtures) string { return "/v1/me" },
		body: obj("display_name", "Renamed Member"), alt: obj("display_name", "Other Member"), idem: true, created: 200},
	{id: "recordConsent", method: http.MethodPost, path: func(*matrixFixtures) string { return "/v1/me/consent" },
		body: obj("version", "2026-11"), alt: obj("version", "2026-12"), idem: true, created: 200, creates: "member_consents"},
	{id: "exportMe", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/me/export" }},
	{id: "getMyCard", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/me/card" }},
	{id: "getMyPoints", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/me/points" }},
	{id: "listMyPointsHistory", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/me/points/history" }},
	// deleteMe ends the session, so it is always exercised last.
	{id: "deleteMe", method: http.MethodDelete, path: func(*matrixFixtures) string { return "/v1/me" }},
}

// serverOps are the API-key routes, each with the scope it needs.
// getCurrentApp needs no scope and is covered by TestCredentialKindMatrix.
var serverOps = []opCase{
	{id: "serverListMembers", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/members" }, scope: "members:read"},
	{id: "serverGetMember", method: http.MethodGet, path: func(f *matrixFixtures) string { return "/v1/members/" + f.member.Member.Id.String() }, scope: "members:read"},
	{id: "serverGetMemberPoints", method: http.MethodGet, path: serverPointsPath, scope: "members:read"},
	{id: "serverPostPoints", method: http.MethodPost, path: serverPointsPath,
		body: obj("type", "earn", "points", 7, "ref_type", "receipt", "ref_id", "R-1"), alt: obj("type", "earn", "points", 8, "ref_type", "receipt", "ref_id", "R-1"),
		idem: true, created: 200, creates: "points_lots", more: []string{"points_ledger"}, scope: "points:write"},
}

func serverPointsPath(f *matrixFixtures) string {
	return "/v1/members/" + f.member.Member.Id.String() + "/points"
}

// otherCoveredOps are operations that act on no tenant resource: health,
// sign-in and the caller's own dashboard account. They are covered by
// auth_handlers_test.go, server_test.go, coverage_auth_test.go,
// coverage_cookie_test.go, member_handlers_test.go (loginWithLine),
// webhook_handler_test.go and invitation_handlers_test.go.
var otherCoveredOps = []string{
	"getHealthz", "getReadyz",
	"dashboardRegister", "dashboardLogin", "dashboardMfa", "dashboardRefresh", "dashboardLogout",
	"getDashboardMe", "changeDashboardPassword", "dashboardLogoutAll", "enrollTotp", "confirmTotp", "disableTotp",
	"listTenants", "getCurrentApp",
	"loginWithLine", "receiveLineWebhook",
	"acceptInvitation", "listSecurityEvents",
	// Staff sign-in acts on no resource until it succeeds; staff_card_test.go
	// and coverage_staff_signin_test.go cover it.
	"staffLoginWithLine",
}

// matrixTables maps each operation table to the security scheme the spec
// must declare for its operations.
var matrixTables = []struct {
	ops    []opCase
	scheme string
}{
	{tenantOps, "dashboardSession"},
	{memberOps, "memberSession"},
	{serverOps, "apiKey"},
	{staffOps, "staffSession"},
}

// deletesWithoutIdempotencyKey lists the write operations the spec exempts
// from the Idempotency-Key header. Every other non-GET operation outside the
// sign-in and profile groups must declare it.
var deletesWithoutIdempotencyKey = []string{"removeTeamMember", "deleteAppChannel", "deleteStore", "deleteStaff", "revokeApiKey", "deleteMe", "eraseMember", "revokeInvitation"}

func TestEveryOperationIsInTheCoverageTable(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		op     opCase
		scheme string
	}
	byID := map[string]entry{}
	for _, tbl := range matrixTables {
		for _, op := range tbl.ops {
			if _, dup := byID[op.id]; dup {
				t.Errorf("duplicate table entry %s", op.id)
			}
			byID[op.id] = entry{op: op, scheme: tbl.scheme}
		}
	}
	for _, id := range otherCoveredOps {
		if _, dup := byID[id]; dup {
			t.Errorf("%s is in a matrix table and in otherCoveredOps", id)
		}
	}
	inSpec := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			id := opKey(op.OperationID)
			inSpec[id] = true
			tc, tabled := byID[id]
			if !tabled && !slices.Contains(otherCoveredOps, id) {
				t.Errorf("%s %s (%s) is not in tenantOps, memberOps, serverOps, staffOps or otherCoveredOps: add cross-tenant, role, idempotency and credential tests", method, path, id)
				continue
			}
			hasIdem := false
			for _, p := range append(slices.Clone(item.Parameters), op.Parameters...) {
				if p.Value != nil && p.Value.Name == "Idempotency-Key" && p.Value.In == "header" {
					hasIdem = true
				}
			}
			if tabled {
				if tc.op.method != method {
					t.Errorf("%s: table says %s, spec says %s", id, tc.op.method, method)
				}
				if tc.op.idem != hasIdem {
					t.Errorf("%s: table idem=%v, spec declares Idempotency-Key=%v", id, tc.op.idem, hasIdem)
				}
				if op.Security == nil || len(*op.Security) != 1 || len((*op.Security)[0]) != 1 {
					t.Errorf("%s: must require exactly one scheme (%s), got %v", id, tc.scheme, op.Security)
				} else if _, ok := (*op.Security)[0][tc.scheme]; !ok {
					t.Errorf("%s: must require %s, got %v", id, tc.scheme, op.Security)
				}
			}
			// Sign-in endpoints and the LINE webhook are retried by their callers
			// in ways that cannot carry a client key (LINE redelivers; the SDK
			// re-exchanges the ID token), so they are exempt.
			authGroup := strings.HasPrefix(path, "/v1/dashboard/auth/") || strings.HasPrefix(path, "/v1/dashboard/me") ||
				strings.HasPrefix(path, "/v1/auth/") || strings.HasPrefix(path, "/webhooks/") ||
				// Staff sign-in is retried with a fresh ID token, and a scan reads
				// a card without changing anything.
				path == "/v1/staff/auth/line" || path == "/v1/staff/scan"
			if method != http.MethodGet && !authGroup && !hasIdem && !slices.Contains(deletesWithoutIdempotencyKey, id) {
				t.Errorf("%s %s (%s) is a write without the Idempotency-Key parameter", method, path, id)
			}
		}
	}
	for id := range byID {
		if !inSpec[id] {
			t.Errorf("table entry %s is not in api/openapi.yaml", id)
		}
	}
	for _, id := range otherCoveredOps {
		if !inSpec[id] {
			t.Errorf("otherCoveredOps entry %s is not in api/openapi.yaml", id)
		}
	}
}

// newMatrixFixtures builds tenant A's resources through the API as owner-a.
func newMatrixFixtures(t *testing.T, w *world) *matrixFixtures {
	t.Helper()
	f := &matrixFixtures{w: w, channel: "developing"}
	f.app = w.createApp("owner-a", w.tenantA, "Cafe")
	owner := w.as("owner-a")
	base := "/v1/dashboard/apps/" + f.app.Id.String()

	r := owner.post(base+"/stores", uuid.NewString(), map[string]any{"name": "Main", "code": "TPE01"})
	want(t, r, http.StatusCreated)
	r.into(t, &f.store)

	r = owner.post(base+"/staff", uuid.NewString(), map[string]any{"display_name": "Mei", "role": "manager"})
	want(t, r, http.StatusCreated)
	r.into(t, &f.staff)

	r = owner.post(base+"/api-keys", uuid.NewString(), map[string]any{"name": "pos", "scopes": []string{"members:read"}})
	want(t, r, http.StatusCreated)
	r.into(t, &f.key)

	r = owner.put(base+"/channels/"+f.channel, uuid.NewString(), map[string]any{"channel_id": fixtureChannelID, "liff_id": fixtureChannelID + "-dev", "channel_secret": "dev-secret"})
	want(t, r, http.StatusOK)
	f.member = w.lineLogin(f.app, fixtureChannelID, fixtureLineUser, fixtureLineName)

	r = owner.post(tenantPath(f)+"/invitations", uuid.NewString(), map[string]any{"email": "pending@example.com", "role": "staff"})
	want(t, r, http.StatusCreated)
	r.into(t, &f.invitation)

	// Member A holds points, so reads have history and redemptions succeed;
	// the account row exists before any matrix write.
	r = owner.post(memberPath(f)+"/points/adjustments", uuid.NewString(), map[string]any{"delta": 100, "note": "opening balance"})
	want(t, r, http.StatusOK)
	f.second = w.lineLogin(f.app, fixtureChannelID, "Ufixturea0000000000000000000004", "Second")
	f.annex = testutil.NewID()
	w.db.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Annex', $3)", f.annex, w.tenantA, "annex-"+f.annex.String()[24:])

	r = owner.post(base+"/staff", uuid.NewString(), map[string]any{"display_name": "Kenji", "role": "cashier"})
	want(t, r, http.StatusCreated)
	r.into(t, &f.cashier)
	f.staffSess = w.staffSession(f.app, fixtureChannelID, fixtureStaffLine, f.staff.InviteCode)
	f.cashierSess = w.staffSession(f.app, fixtureChannelID, fixtureCashierLine, f.cashier.InviteCode)
	return f
}

// LINE accounts of app A's staff.
const (
	fixtureStaffLine   = "Ufixturestaff000000000000000001"
	fixtureCashierLine = "Ufixturestaff000000000000000002"
)

// staffLogin signs a LINE account in to the staff app of app through
// /v1/staff/auth/line; code is the invite code, empty after binding.
func (w *world) staffLogin(app api.App, channelID, sub, code string) tresp {
	w.t.Helper()
	token := "staff-idtoken-" + sub + "-" + uuid.NewString()
	w.verifier.Add(token, lineauth.Claims{Sub: sub, Name: "Staff " + sub, Aud: channelID})
	body := map[string]any{"app_id": app.Id, "id_token": token}
	if code != "" {
		body["invite_code"] = code
	}
	return w.call(http.MethodPost, "/v1/staff/auth/line", "", "", "", body)
}

func (w *world) staffSession(app api.App, channelID, sub, code string) api.StaffSession {
	w.t.Helper()
	r := w.staffLogin(app, channelID, sub, code)
	if r.code != http.StatusOK {
		w.t.Fatalf("staff login: %d %s", r.code, r.body)
	}
	var sess api.StaffSession
	r.into(w.t, &sess)
	return sess
}

// fixtureMarkers are strings from tenant A's data that must never appear in
// a response to an outsider.
func (f *matrixFixtures) markers() []string {
	return []string{"Cafe", "Main", "TPE01", "Mei", "Kenji", "Annex", "goodwill", "opening balance", f.key.Key, f.key.Prefix, fixtureChannelID, "dev-secret", f.app.Slug, "pending@example.com", f.invitation.InviteUrl,
		fixtureLineName, fixtureLineUser, f.member.Member.Id.String(), f.member.SessionToken, f.staffSess.SessionToken, f.staff.Id.String()}
}

// tenantTables are every table that carries tenant data, with the column
// that scopes it.
var tenantTables = []struct{ name, col string }{
	{"tenants", "id"},
	{"tenant_users", "tenant_id"},
	{"apps", "tenant_id"},
	{"app_channels", "tenant_id"},
	{"stores", "tenant_id"},
	{"staff", "tenant_id"},
	{"api_keys", "tenant_id"},
	{"audit_log", "tenant_id"},
	{"idempotency_keys", "tenant_id"},
	{"invitations", "tenant_id"},
	{"members", "tenant_id"},
	{"member_consents", "tenant_id"},
	{"webhook_inbound_events", "tenant_id"},
	{"points_rules", "tenant_id"},
	{"points_accounts", "tenant_id"},
	{"points_lots", "tenant_id"},
	{"points_ledger", "tenant_id"},
}

// snapshot fingerprints every row of tenantID in every tenant table.
func (w *world) snapshot(tenantID uuid.UUID) map[string]string {
	w.t.Helper()
	out := map[string]string{}
	for _, tbl := range tenantTables {
		var sum string
		q := fmt.Sprintf("SELECT md5(coalesce(string_agg(t::text, '|' ORDER BY t::text), '')) FROM %s t WHERE %s = $1", tbl.name, tbl.col)
		if err := w.db.Admin.QueryRow(context.Background(), q, tenantID).Scan(&sum); err != nil {
			w.t.Fatalf("snapshot %s: %v", tbl.name, err)
		}
		out[tbl.name] = sum
	}
	return out
}

// counts returns the row count of tenantID in every tenant table.
func (w *world) counts(tenantID uuid.UUID) map[string]int {
	w.t.Helper()
	out := map[string]int{}
	for _, tbl := range tenantTables {
		var n int
		q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tbl.name, tbl.col)
		if err := w.db.Admin.QueryRow(context.Background(), q, tenantID).Scan(&n); err != nil {
			w.t.Fatalf("count %s: %v", tbl.name, err)
		}
		out[tbl.name] = n
	}
	return out
}

func assertUnchanged(t *testing.T, before, after map[string]string, context string) {
	t.Helper()
	assertUnchangedExcept(t, before, after, context)
}

// assertUnchangedExcept is assertUnchanged for flows that legitimately touch
// a few tables, such as api_keys.last_used_at, which a successful key
// authentication updates asynchronously.
func assertUnchangedExcept(t *testing.T, before, after map[string]string, context string, except ...string) {
	t.Helper()
	for _, tbl := range tenantTables {
		if slices.Contains(except, tbl.name) {
			continue
		}
		if before[tbl.name] != after[tbl.name] {
			t.Errorf("%s: table %s changed", context, tbl.name)
		}
	}
}

// send runs the operation as token with the given body and an Idempotency-Key
// when the spec requires one.
func (f *matrixFixtures) send(op opCase, token string, body func(*matrixFixtures) any, key string) tresp {
	return f.sendTo(op, op.path(f), token, body, key)
}

func (f *matrixFixtures) sendTo(op opCase, path, token string, body func(*matrixFixtures) any, key string) tresp {
	if !op.idem {
		key = ""
	}
	return f.w.call(op.method, path, token, "", key, bodyOf(f, body))
}

// mismatch sends the idempotency mismatch request: the alternative body, or
// for a body-less write the same key on another target.
func (f *matrixFixtures) mismatch(t *testing.T, op opCase, token, key string) tresp {
	t.Helper()
	switch {
	case op.alt != nil:
		return f.send(op, token, op.alt, key)
	case op.altPath != nil:
		return f.sendTo(op, op.altPath(f), token, nil, key)
	}
	t.Fatalf("%s: an idempotent write needs alt or altPath", op.id)
	return tresp{}
}

func bodyOf(f *matrixFixtures, body func(*matrixFixtures) any) any {
	if body == nil {
		return nil
	}
	return body(f)
}

// Invariant: a user from another tenant and a user with no tenant get 404
// from every tenant-scoped operation, the body names nothing of tenant A,
// and nothing in tenant A changes.
func TestCrossTenantMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	before := w.snapshot(w.tenantA)

	for _, outsider := range []string{"owner-b", "loner"} {
		for _, op := range tenantOps {
			t.Run(op.id+"/"+outsider, func(t *testing.T) {
				r := f.send(op, outsider, op.body, uuid.NewString())
				if r.code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404; body %s", r.code, r.body)
				}
				if code := r.errCode(t); code != "not_found" {
					t.Errorf("code = %s, want not_found", code)
				}
				// The random hex request ID can spell a marker such as "cafe".
				scanned := withoutRequestID(r.body)
				for _, m := range f.markers() {
					if bytes.Contains(scanned, []byte(m)) {
						t.Errorf("body leaks %q: %s", m, r.body)
					}
				}
			})
		}
	}
	assertUnchanged(t, before, w.snapshot(w.tenantA), "after outsider calls")

	// Outsiders see only their own memberships.
	list := w.as("owner-b").get("/v1/dashboard/tenants")
	want(t, list, http.StatusOK)
	if bytes.Contains(list.body, []byte(w.tenantA.String())) || bytes.Contains(list.body, []byte("Alpha")) {
		t.Errorf("owner-b's tenant list leaks tenant A: %s", list.body)
	}
	empty := w.as("loner").get("/v1/dashboard/tenants")
	want(t, empty, http.StatusOK)
	if strings.TrimSpace(string(empty.body)) != "[]" {
		t.Errorf("loner's tenant list = %s, want []", empty.body)
	}
}

// Invariant: staff can read everything except data-rights exports and write
// nothing; admins cannot grant, change or remove owner roles. Refused
// requests change nothing.
func TestRoleMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	before := w.snapshot(w.tenantA)

	for _, op := range tenantOps {
		t.Run(op.id+"/staff", func(t *testing.T) {
			r := f.send(op, "staff-a", op.body, uuid.NewString())
			if !op.write() && principal.RoleStaff.AtLeast(op.min) {
				if r.code != http.StatusOK {
					t.Fatalf("staff read: status = %d, want 200; body %s", r.code, r.body)
				}
				return
			}
			if r.code != http.StatusForbidden {
				t.Fatalf("staff %s: status = %d, want 403; body %s", strings.ToLower(op.method), r.code, r.body)
			}
			if code := r.errCode(t); code != "forbidden" {
				t.Errorf("code = %s, want forbidden", code)
			}
		})
	}
	assertUnchanged(t, before, w.snapshot(w.tenantA), "after staff requests")

	team := tenantPath(f) + "/team"
	admin := w.as("admin-a")
	ownerOnly := []struct {
		name string
		r    func() tresp
	}{
		{"admin grants owner", func() tresp {
			return admin.post(tenantPath(f)+"/invitations", uuid.NewString(), map[string]any{"email": "loner@example.com", "role": "owner"})
		}},
		{"admin promotes to owner", func() tresp {
			return admin.patch(team+"/"+w.staffA.String(), uuid.NewString(), map[string]any{"role": "owner"})
		}},
		{"admin changes any role", func() tresp {
			return admin.patch(team+"/"+w.staffA.String(), uuid.NewString(), map[string]any{"role": "admin"})
		}},
		{"admin demotes owner", func() tresp {
			return admin.patch(team+"/"+w.ownerA.String(), uuid.NewString(), map[string]any{"role": "admin"})
		}},
		{"admin removes owner", func() tresp { return admin.del(team + "/" + w.ownerA.String()) }},
	}
	for _, tc := range ownerOnly {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.r()
			if r.code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %s", r.code, r.body)
			}
		})
	}
	assertUnchanged(t, before, w.snapshot(w.tenantA), "after admin owner-role attempts")

	// Positive controls: every operation accepts its minimum role on a read.
	for _, op := range tenantOps {
		if op.write() {
			continue
		}
		for _, token := range []string{"admin-a", "owner-a"} {
			if r := f.send(op, token, nil, ""); r.code != http.StatusOK {
				t.Errorf("%s as %s: status = %d; body %s", op.id, token, r.code, r.body)
			}
		}
	}
}

// Invariant: a write with an Idempotency-Key runs once. Replaying the key
// with the same payload returns the identical response and adds no row;
// the same key with a different payload is 422; no key is 400.
func TestIdempotencyMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	// The fixture app would otherwise hit the free plan's limit in createApp.
	w.db.Exec(t, "UPDATE tenants SET plan = 'agency' WHERE id = $1", w.tenantA)

	for _, op := range tenantOps {
		if !op.idem {
			continue
		}
		t.Run(op.id, func(t *testing.T) {
			before := w.counts(w.tenantA)
			key := uuid.NewString()

			first := f.send(op, "owner-a", op.body, key)
			if first.code != op.created {
				t.Fatalf("first: status = %d, want %d; body %s", first.code, op.created, first.body)
			}
			replay := f.send(op, "owner-a", op.body, key)
			if replay.code != op.created {
				t.Fatalf("replay: status = %d, want %d; body %s", replay.code, op.created, replay.body)
			}
			if !bytes.Equal(first.body, replay.body) {
				t.Errorf("replay body differs:\n%s\n%s", first.body, replay.body)
			}
			mismatch := f.mismatch(t, op, "owner-a", key)
			if mismatch.code != http.StatusUnprocessableEntity {
				t.Fatalf("mismatch: status = %d, want 422; body %s", mismatch.code, mismatch.body)
			}
			if code := mismatch.errCode(t); code != "idempotency_mismatch" {
				t.Errorf("mismatch code = %s", code)
			}
			noKey := w.call(op.method, op.path(f), "owner-a", "", "", bodyOf(f, op.body))
			if noKey.code != http.StatusBadRequest {
				t.Fatalf("no key: status = %d, want 400; body %s", noKey.code, noKey.body)
			}
			// The key is scoped to the principal: for admin-a it is unused, so
			// the write runs again (a fresh audit entry) or is refused by a
			// rule, but never replays owner-a's stored outcome.
			other := f.send(op, "admin-a", op.body, key)
			if other.code != op.created && (other.code < 400 || other.code >= 500) {
				t.Fatalf("admin-a with the same key: status = %d; body %s", other.code, other.body)
			}
			runs := 1
			if other.code == op.created {
				runs = 2
			}

			after := w.counts(w.tenantA)
			for _, tbl := range tenantTables {
				wantDelta := 0
				switch tbl.name {
				case "audit_log", "idempotency_keys":
					wantDelta = runs
				case op.creates:
					wantDelta = runs
					if op.upsert {
						wantDelta = 1
					}
				default:
					if slices.Contains(op.more, tbl.name) {
						wantDelta = runs
					}
				}
				if got := after[tbl.name] - before[tbl.name]; got != wantDelta {
					t.Errorf("%s: %d new rows, want %d (replay, mismatch and missing key must add none)", tbl.name, got, wantDelta)
				}
			}
		})
	}
}

// Invariant: a server API key is not a dashboard session and a dashboard
// session is not an API key. Each is refused with 401 on the other's routes,
// as is a request with no credentials at all.
func TestCredentialKindMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	before := w.snapshot(w.tenantA)

	dashboard := append(slices.Clone(tenantOps), opCase{id: "listTenants", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/dashboard/tenants" }})
	for _, op := range dashboard {
		t.Run(op.id, func(t *testing.T) {
			var body any
			if op.body != nil {
				body = op.body(f)
			}
			key := ""
			if op.idem {
				key = uuid.NewString()
			}
			cases := map[string]tresp{
				"api key":        w.call(op.method, op.path(f), "", f.key.Key, key, body),
				"no credentials": w.call(op.method, op.path(f), "", "", key, body),
				// Member and staff sessions share the signing key, not the kind.
				"member token": w.call(op.method, op.path(f), f.member.SessionToken, "", key, body),
				"staff token":  w.call(op.method, op.path(f), f.staffSess.SessionToken, "", key, body),
				"api key and bearer": func() tresp {
					// X-API-Key wins in the middleware; the key is still not a user.
					return w.call(op.method, op.path(f), "owner-a", f.key.Key, key, body)
				}(),
			}
			for name, r := range cases {
				if r.code != http.StatusUnauthorized {
					t.Errorf("%s: status = %d, want 401; body %s", name, r.code, r.body)
					continue
				}
				if code := r.errCode(t); code != "unauthorized" {
					t.Errorf("%s: code = %s, want unauthorized", name, code)
				}
			}
		})
	}
	// Credentials in the wrong header are not credentials.
	swapped := w.call(http.MethodGet, "/v1/dashboard/tenants", f.key.Key, "", "", nil)
	want(t, swapped, http.StatusUnauthorized)
	if r := w.call(http.MethodGet, "/v1/app", "", "owner-a", "", nil); r.code != http.StatusUnauthorized {
		t.Errorf("dashboard token in X-API-Key: %d", r.code)
	}

	// The server API refuses dashboard sessions and accepts the key.
	for _, token := range []string{"owner-a", "admin-a", "staff-a", "owner-b"} {
		if r := w.as(token).get("/v1/app"); r.code != http.StatusUnauthorized {
			t.Errorf("/v1/app with dashboard token %s: %d", token, r.code)
		}
	}
	if r := w.call(http.MethodGet, "/v1/app", "", "", "", nil); r.code != http.StatusUnauthorized {
		t.Errorf("/v1/app without credentials: %d", r.code)
	}
	ok := w.call(http.MethodGet, "/v1/app", "", f.key.Key, "", nil)
	want(t, ok, http.StatusOK)

	// api_keys.last_used_at is touched asynchronously by the successful call,
	// so compare everything else.
	after := w.snapshot(w.tenantA)
	for _, tbl := range tenantTables {
		if tbl.name != "api_keys" && before[tbl.name] != after[tbl.name] {
			t.Errorf("table %s changed by refused requests", tbl.name)
		}
	}
}

var requestIDField = regexp.MustCompile(`"request_id":"[^"]*"`)

// withoutRequestID blanks the request_id value so leak checks never match
// the random hex identifier.
func withoutRequestID(body []byte) []byte {
	return requestIDField.ReplaceAll(body, []byte(`"request_id":""`))
}
