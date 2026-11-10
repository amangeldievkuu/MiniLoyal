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
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// principalTokens maps bearer tokens or API keys to principals, standing in
// for the member, staff and API key authenticators in these tests.
type principalTokens map[string]principal.Principal

func (f principalTokens) lookup(token string) (principal.Principal, error) {
	if p, ok := f[token]; ok {
		return p, nil
	}
	return principal.Principal{}, apierr.Unauthorized("invalid token")
}

func (f principalTokens) AuthenticateMember(_ context.Context, t string) (principal.Principal, error) {
	p, err := f.lookup(t)
	if err == nil && !p.IsMember() {
		return principal.Principal{}, apierr.Unauthorized("invalid token")
	}
	return p, err
}

func (f principalTokens) AuthenticateStaff(_ context.Context, t string) (principal.Principal, error) {
	p, err := f.lookup(t)
	if err == nil && !p.IsStaff() {
		return principal.Principal{}, apierr.Unauthorized("invalid token")
	}
	return p, err
}

func (f principalTokens) AuthenticateAPIKey(_ context.Context, t string) (principal.Principal, error) {
	p, err := f.lookup(t)
	if err == nil && !p.IsAPIKey() {
		return principal.Principal{}, apierr.Unauthorized("invalid key")
	}
	return p, err
}

// syncBuffer collects log output written from request goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type pointsWorld struct {
	t      *testing.T
	d      *testutil.DB
	srv    *httptest.Server
	codec  *card.Codec
	logs   *syncBuffer
	tenant uuid.UUID
	app    uuid.UUID
	member uuid.UUID
	lineID string
	// principals keyed by the token the tests send
	tokens   principalTokens
	cashier  principal.Principal
	manager  principal.Principal
	foreignM uuid.UUID // a member of another app in the same tenant
}

func newPointsWorld(t *testing.T) *pointsWorld {
	t.Helper()
	d := testutil.NewDB(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	keyring := crypto.Static(bytes.Repeat([]byte{5}, 32))
	codec, err := card.New(bytes.Repeat([]byte{7}, 32), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	tenants := tenant.New(d.App, keyring, clock.Real{}, log)
	w := &pointsWorld{t: t, d: d, codec: codec, logs: logs, tokens: principalTokens{}}

	w.tenant = d.NewTenant(t, "Alpha")
	w.app = w.newApp(w.tenant)
	w.member, w.lineID = w.newMember(w.tenant, w.app)
	otherApp := w.newApp(w.tenant)
	w.foreignM, _ = w.newMember(w.tenant, otherApp)

	users := map[string]uuid.UUID{}
	for token, role := range map[string]string{"owner": "owner", "admin": "admin", "staff-user": "staff"} {
		users[token] = w.user(w.tenant, role)
	}
	betaTenant := d.NewTenant(t, "Beta")
	users["outsider"] = w.user(betaTenant, "owner")

	w.cashier = principal.Principal{Kind: principal.KindStaff, StaffID: testutil.NewID(), StaffRole: "cashier", AppID: w.app, TenantID: w.tenant}
	w.manager = principal.Principal{Kind: principal.KindStaff, StaffID: testutil.NewID(), StaffRole: "manager", AppID: w.app, TenantID: w.tenant}
	w.tokens["cashier"] = w.cashier
	w.tokens["manager"] = w.manager
	w.tokens["member"] = principal.Principal{Kind: principal.KindMember, MemberID: w.member, AppID: w.app, TenantID: w.tenant}
	w.tokens["key-read"] = principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: w.tenant, AppID: w.app, Scopes: []string{"members:read"}}
	w.tokens["key-write"] = principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: w.tenant, AppID: w.app, Scopes: []string{"points:write"}}

	s := New(Deps{
		Config:    &config.Config{Env: config.Development},
		Log:       log,
		DB:        d.App,
		Version:   "test",
		Dashboard: fakeDashboard{tokens: users},
		Members:   w.tokens,
		Staff:     w.tokens,
		APIKeys:   w.tokens,
		Tenants:   tenants,
		Card:      codec,
		Points:    points.New(points.Deps{Pool: d.App, Tenants: tenants, Keyring: keyring, Card: codec, Clock: clock.Real{}, Log: log}),
	})
	w.srv = httptest.NewServer(s.Handler())
	t.Cleanup(w.srv.Close)
	return w
}

func (w *pointsWorld) newApp(tenantID uuid.UUID) uuid.UUID {
	id := testutil.NewID()
	w.d.Exec(w.t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', $3)", id, tenantID, "cafe-"+id.String()[24:])
	return id
}

func (w *pointsWorld) newMember(tenantID, appID uuid.UUID) (uuid.UUID, string) {
	id := testutil.NewID()
	line := "Uline" + strings.ReplaceAll(id.String(), "-", "")
	w.d.Exec(w.t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", id, tenantID, appID, line)
	return id, line
}

func (w *pointsWorld) user(tenantID uuid.UUID, role string) uuid.UUID {
	id := testutil.NewID()
	w.d.Exec(w.t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", id, id.String()+"@example.com")
	w.d.Exec(w.t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, id, role)
	return id
}

func (w *pointsWorld) scan(st principal.Principal, memberID uuid.UUID) string {
	tok, _, err := w.codec.IssueScanToken(st.StaffID, memberID, st.AppID, st.TenantID, 1)
	if err != nil {
		w.t.Fatal(err)
	}
	return tok
}

type presp struct {
	status int
	body   []byte
}

func (r presp) into(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r presp) code(t *testing.T) string {
	t.Helper()
	var e api.Error
	r.into(t, &e)
	return e.Code
}

// call sends a request; auth is "bearer:<token>", "key:<key>" or "".
func (w *pointsWorld) call(method, path, auth, idemKey string, body any) presp {
	w.t.Helper()
	var buf io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, w.srv.URL+path, buf)
	req.Header.Set("Content-Type", "application/json")
	if kind, val, ok := strings.Cut(auth, ":"); ok {
		if kind == "key" {
			req.Header.Set("X-API-Key", val)
		} else {
			req.Header.Set("Authorization", "Bearer "+val)
		}
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	res, err := w.srv.Client().Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return presp{status: res.StatusCode, body: b}
}

func TestStaffEarnAndRedeemOverHTTP(t *testing.T) {
	w := newPointsWorld(t)
	tok := w.scan(w.cashier, w.member)

	earn := w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:cashier", "earn-1", map[string]any{"scan_token": tok, "spend_amount": 1250})
	if earn.status != http.StatusOK {
		t.Fatalf("earn: %d %s", earn.status, earn.body)
	}
	var tx api.PointsTransaction
	earn.into(t, &tx)
	if tx.Points != 12 || tx.Kind != api.PointsKindEarn || tx.BalanceAfter != 12 || tx.ActorType != "staff" {
		t.Fatalf("earn txn = %+v", tx)
	}
	// Retry with the same key and body replays; another body is refused.
	replay := w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:cashier", "earn-1", map[string]any{"scan_token": tok, "spend_amount": 1250})
	if replay.status != http.StatusOK || !bytes.Equal(replay.body, earn.body) {
		t.Fatalf("replay: %d %s", replay.status, replay.body)
	}
	if r := w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:cashier", "earn-1", map[string]any{"scan_token": tok, "spend_amount": 9999}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: %d %s", r.status, r.body)
	}
	if r := w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:cashier", "", map[string]any{"scan_token": tok, "spend_amount": 100}); r.status != http.StatusBadRequest {
		t.Fatalf("no key: %d %s", r.status, r.body)
	}
	// Cashiers cannot give fixed points; managers can.
	if r := w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:cashier", "earn-2", map[string]any{"scan_token": tok, "points": 50}); r.status != http.StatusForbidden {
		t.Fatalf("cashier fixed points: %d %s", r.status, r.body)
	}
	if r := w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:manager", "earn-3", map[string]any{"scan_token": w.scan(w.manager, w.member), "points": 50}); r.status != http.StatusOK {
		t.Fatalf("manager fixed points: %d %s", r.status, r.body)
	}
	// Someone else's scan, another app's scan, garbage.
	foreignTok, _, _ := w.codec.IssueScanToken(w.cashier.StaffID, w.member, uuid.New(), w.tenant, 1)
	for name, token := range map[string]string{"another staff member": w.scan(w.manager, w.member), "another app": foreignTok, "garbage": "st1.x.y"} {
		r := w.call(http.MethodPost, "/v1/staff/points/redeem", "bearer:cashier", "r-"+name, map[string]any{"scan_token": token, "points": 1})
		if r.status != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}

	redeem := w.call(http.MethodPost, "/v1/staff/points/redeem", "bearer:cashier", "redeem-1", map[string]any{"scan_token": tok, "points": 60})
	if redeem.status != http.StatusOK {
		t.Fatalf("redeem: %d %s", redeem.status, redeem.body)
	}
	redeem.into(t, &tx)
	if tx.Points != -60 || tx.BalanceAfter != 2 {
		t.Fatalf("redeem txn = %+v", tx)
	}
	// One scan pays for one redemption; a fresh scan is needed for the next.
	if r := w.call(http.MethodPost, "/v1/staff/points/redeem", "bearer:cashier", "redeem-2", map[string]any{"scan_token": tok, "points": 1}); r.status != http.StatusConflict || r.code(t) != "scan_used" {
		t.Fatalf("second redemption on one scan: %d %s", r.status, r.body)
	}
	if r := w.call(http.MethodPost, "/v1/staff/points/redeem", "bearer:cashier", "redeem-2b", map[string]any{"scan_token": w.scan(w.cashier, w.member), "points": 3}); r.status != http.StatusConflict || r.code(t) != "insufficient_points" {
		t.Fatalf("overspend: %d %s", r.status, r.body)
	}
	// A member token cannot act as staff, and a staff token cannot reach member routes.
	if r := w.call(http.MethodPost, "/v1/staff/points/redeem", "bearer:member", "redeem-3", map[string]any{"scan_token": tok, "points": 1}); r.status != http.StatusUnauthorized {
		t.Errorf("member token on a staff route: %d", r.status)
	}
	if r := w.call(http.MethodGet, "/v1/me/points", "bearer:cashier", "", nil); r.status != http.StatusUnauthorized {
		t.Errorf("staff token on a member route: %d", r.status)
	}

	// The member sees the result.
	var sum api.PointsSummary
	r := w.call(http.MethodGet, "/v1/me/points", "bearer:member", "", nil)
	r.into(t, &sum)
	if r.status != http.StatusOK || sum.Balance != 2 || sum.LifetimeEarned != 62 || sum.Rules.SpendPerPoint != 100 {
		t.Fatalf("my points: %d %+v", r.status, sum)
	}
	var page api.PointsHistoryPage
	r = w.call(http.MethodGet, "/v1/me/points/history?limit=2", "bearer:member", "", nil)
	r.into(t, &page)
	if r.status != http.StatusOK || len(page.Items) != 2 || page.Items[0].Kind != api.PointsKindRedeem || page.NextCursor == nil {
		t.Fatalf("history page 1: %d %s", r.status, r.body)
	}
	r = w.call(http.MethodGet, "/v1/me/points/history?limit=2&cursor="+*page.NextCursor, "bearer:member", "", nil)
	page = api.PointsHistoryPage{}
	r.into(t, &page)
	if r.status != http.StatusOK || len(page.Items) != 1 || page.Items[0].Points != 12 || page.NextCursor != nil {
		t.Fatalf("history page 2: %d %s", r.status, r.body)
	}

	// Logs at info never carry a scan token or the member's LINE user ID.
	logs := w.logs.String()
	if strings.Contains(logs, tok) || strings.Contains(logs, "st1.") || strings.Contains(logs, w.lineID) {
		t.Fatalf("logs leak a scan token or LINE user ID:\n%s", logs)
	}
}

func TestDashboardPointsOverHTTP(t *testing.T) {
	w := newPointsWorld(t)
	appPath := "/v1/dashboard/apps/" + w.app.String()
	memberPath := "/v1/dashboard/members/" + w.member.String()
	rules := map[string]any{"currency": "TWD", "spend_per_point": 50, "points_per_unit": 2, "min_spend": 0, "expiry_days": 365, "enabled": true}

	// Rules: defaults, then a saved rule.
	var got api.PointsRules
	r := w.call(http.MethodGet, appPath+"/points-rules", "bearer:staff-user", "", nil)
	r.into(t, &got)
	if r.status != http.StatusOK || got.SpendPerPoint != 100 || got.UpdatedAt != nil {
		t.Fatalf("default rules: %d %s", r.status, r.body)
	}
	if r := w.call(http.MethodPut, appPath+"/points-rules", "bearer:staff-user", "rules-0", rules); r.status != http.StatusForbidden {
		t.Fatalf("staff role saving rules: %d", r.status)
	}
	r = w.call(http.MethodPut, appPath+"/points-rules", "bearer:admin", "rules-1", rules)
	r.into(t, &got)
	if r.status != http.StatusOK || got.SpendPerPoint != 50 || got.ExpiryDays == nil || *got.ExpiryDays != 365 || got.UpdatedAt == nil {
		t.Fatalf("save rules: %d %s", r.status, r.body)
	}
	bad := map[string]any{"currency": "twdollar", "spend_per_point": 1, "points_per_unit": 1, "min_spend": 0, "enabled": true}
	if r := w.call(http.MethodPut, appPath+"/points-rules", "bearer:admin", "rules-2", bad); r.status != http.StatusBadRequest {
		t.Fatalf("invalid currency: %d %s", r.status, r.body)
	}

	// Adjust: owners and admins only, audited.
	adj := w.call(http.MethodPost, memberPath+"/points/adjustments", "bearer:admin", "adj-1", map[string]any{"delta": 30, "note": "goodwill"})
	if adj.status != http.StatusOK {
		t.Fatalf("adjust: %d %s", adj.status, adj.body)
	}
	if r := w.call(http.MethodPost, memberPath+"/points/adjustments", "bearer:staff-user", "adj-2", map[string]any{"delta": 30, "note": "x"}); r.status != http.StatusForbidden {
		t.Fatalf("staff role adjusting: %d", r.status)
	}
	if r := w.call(http.MethodPost, memberPath+"/points/adjustments", "bearer:admin", "adj-3", map[string]any{"delta": -31, "note": "too much"}); r.status != http.StatusConflict {
		t.Fatalf("negative beyond balance: %d %s", r.status, r.body)
	}
	if r := w.call(http.MethodPost, memberPath+"/points/adjustments", "bearer:admin", "adj-4", map[string]any{"delta": 0, "note": "zero"}); r.status != http.StatusBadRequest {
		t.Fatalf("zero delta: %d %s", r.status, r.body)
	}
	var audited int
	_ = w.d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE action IN ('points.adjust', 'points.rules') AND tenant_id = $1", w.tenant).Scan(&audited)
	if audited != 2 {
		t.Fatalf("audit rows for adjust and rules = %d, want 2", audited)
	}

	// Reads for every team role.
	var sum api.PointsSummary
	r = w.call(http.MethodGet, memberPath+"/points", "bearer:staff-user", "", nil)
	r.into(t, &sum)
	if r.status != http.StatusOK || sum.Balance != 30 || len(sum.Expiring) != 0 {
		t.Fatalf("member summary: %d %s", r.status, r.body)
	}
	if r := w.call(http.MethodGet, memberPath+"/points/history", "bearer:staff-user", "", nil); r.status != http.StatusOK {
		t.Fatalf("member history: %d", r.status)
	}

	// Another tenant learns nothing.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, appPath + "/points-rules"},
		{http.MethodPut, appPath + "/points-rules"},
		{http.MethodGet, memberPath + "/points"},
		{http.MethodGet, memberPath + "/points/history"},
		{http.MethodPost, memberPath + "/points/adjustments"},
	} {
		var body any
		if c.method == http.MethodPut {
			body = rules
		} else if c.method == http.MethodPost {
			body = map[string]any{"delta": 5, "note": "x"}
		}
		r := w.call(c.method, c.path, "bearer:outsider", "out-"+c.method+c.path, body)
		if r.status != http.StatusNotFound || bytes.Contains(r.body, []byte(w.member.String())) {
			t.Errorf("outsider %s %s: %d %s", c.method, c.path, r.status, r.body)
		}
	}
	var balance int64
	_ = w.d.Admin.QueryRow(context.Background(), "SELECT balance FROM points_accounts WHERE member_id = $1", w.member).Scan(&balance)
	if balance != 30 {
		t.Fatalf("balance after outsider attempts = %d", balance)
	}
}

func TestServerPointsOverHTTP(t *testing.T) {
	w := newPointsWorld(t)
	path := "/v1/members/" + w.member.String() + "/points"

	if r := w.call(http.MethodPost, path, "key:key-read", "s-1", map[string]any{"type": "earn", "points": 5}); r.status != http.StatusForbidden {
		t.Fatalf("members:read key writing: %d %s", r.status, r.body)
	}
	earn := w.call(http.MethodPost, path, "key:key-write", "s-2", map[string]any{"type": "earn", "spend_amount": 999, "ref_type": "receipt", "ref_id": "R-77"})
	var tx api.PointsTransaction
	earn.into(t, &tx)
	if earn.status != http.StatusOK || tx.Points != 9 || tx.RefId == nil || *tx.RefId != "R-77" {
		t.Fatalf("server earn: %d %s", earn.status, earn.body)
	}
	if r := w.call(http.MethodGet, path, "key:key-write", "", nil); r.status != http.StatusForbidden {
		t.Fatalf("points:write key reading: %d", r.status)
	}
	var sum api.PointsSummary
	r := w.call(http.MethodGet, path, "key:key-read", "", nil)
	r.into(t, &sum)
	if r.status != http.StatusOK || sum.Balance != 9 {
		t.Fatalf("server summary: %d %s", r.status, r.body)
	}
	foreign := "/v1/members/" + w.foreignM.String() + "/points"
	if r := w.call(http.MethodGet, foreign, "key:key-read", "", nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign-app member read: %d", r.status)
	}
	if r := w.call(http.MethodPost, foreign, "key:key-write", "s-3", map[string]any{"type": "earn", "points": 1}); r.status != http.StatusNotFound {
		t.Fatalf("foreign-app member write: %d", r.status)
	}
	if r := w.call(http.MethodPost, path, "key:key-write", "s-4", map[string]any{"type": "gift", "points": 1}); r.status != http.StatusBadRequest {
		t.Fatalf("unknown type: %d %s", r.status, r.body)
	}
	// A dashboard token or member token cannot use the server routes.
	if r := w.call(http.MethodGet, path, "bearer:member", "", nil); r.status != http.StatusUnauthorized {
		t.Errorf("member token on the server route: %d", r.status)
	}
}
