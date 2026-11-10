package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/staffauth"
)

// staffOps are the staff app routes. A staff session reaches only members of
// its own app, and points actions additionally need a scan token issued to
// that staff member for that member (f.scan).
var staffOps = []opCase{
	{id: "getStaffMe", method: http.MethodGet, path: func(*matrixFixtures) string { return "/v1/staff/me" }},
	{id: "staffScan", method: http.MethodPost, path: func(*matrixFixtures) string { return "/v1/staff/scan" },
		body: func(f *matrixFixtures) any { return map[string]any{"qr": f.qrOf(f.member)} }},
	{id: "staffEarnPoints", method: http.MethodPost, path: func(*matrixFixtures) string { return "/v1/staff/points/earn" },
		body: func(f *matrixFixtures) any { return map[string]any{"scan_token": f.scan, "spend_amount": 500} },
		alt:  func(f *matrixFixtures) any { return map[string]any{"scan_token": f.scan, "spend_amount": 600} },
		idem: true, created: 200, creates: "points_lots", more: []string{"points_ledger"}},
	{id: "staffRedeemPoints", method: http.MethodPost, path: func(*matrixFixtures) string { return "/v1/staff/points/redeem" },
		body: func(f *matrixFixtures) any { return map[string]any{"scan_token": f.scan, "points": 3} },
		alt:  func(f *matrixFixtures) any { return map[string]any{"scan_token": f.scan, "points": 4} },
		idem: true, created: 200, more: []string{"points_ledger"}},
}

// qrOf returns the member's current card code.
func (f *matrixFixtures) qrOf(m api.MemberSession) string {
	f.w.t.Helper()
	r := f.w.call(http.MethodGet, "/v1/me/card", m.SessionToken, "", "", nil)
	if r.code != http.StatusOK {
		f.w.t.Fatalf("card: %d %s", r.code, r.body)
	}
	var c api.MemberCard
	r.into(f.w.t, &c)
	return c.Qr
}

// scanAs scans the member's card as the staff session and returns the scan token.
func (f *matrixFixtures) scanAs(staffToken string, m api.MemberSession) string {
	f.w.t.Helper()
	r := f.w.call(http.MethodPost, "/v1/staff/scan", staffToken, "", "", map[string]any{"qr": f.qrOf(m)})
	if r.code != http.StatusOK {
		f.w.t.Fatalf("scan: %d %s", r.code, r.body)
	}
	var res api.ScanResult
	r.into(f.w.t, &res)
	return res.ScanToken
}

// forgedStaffToken signs a staff session with the server's key; every claim
// is right except the ones the case names.
func forgedStaffToken(t *testing.T, sub, tenantID, appID uuid.UUID, typ string, iat time.Time, ttl time.Duration) string {
	t.Helper()
	tok, err := jwt.NewBuilder().
		Issuer(staffauth.DefaultIssuer).
		Subject(sub.String()).
		IssuedAt(iat).
		Expiration(iat.Add(ttl)).
		Claim("typ", typ).
		Claim("tenant_id", tenantID.String()).
		Claim("app_id", appID.String()).
		Claim("iat_ms", iat.UnixMilli()).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), fullSigningKey))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

// boundStaff creates a staff member in app with role and signs them in.
func boundStaff(t *testing.T, w *world, owner string, app api.App, channelID, sub, name, role string) (api.StaffCreated, api.StaffSession) {
	t.Helper()
	r := w.as(owner).post("/v1/dashboard/apps/"+app.Id.String()+"/staff", uuid.NewString(), map[string]any{"display_name": name, "role": role})
	want(t, r, http.StatusCreated)
	var st api.StaffCreated
	r.into(t, &st)
	return st, w.staffSession(app, channelID, sub, st.InviteCode)
}

func (w *world) ledgerRows(memberID uuid.UUID) int {
	w.t.Helper()
	var n int
	if err := w.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM points_ledger WHERE member_id = $1", memberID).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// Invariant: a staff session acts only inside its own app. Its own
// operations succeed and name only its app's data; a staff member of app B
// cannot scan, earn for or redeem from app A's members, even holding app
// A's scan token or a well-signed token naming them; a cashier of app A
// cannot use the manager's scan token. Dashboard, member and API-key
// credentials, and staff tokens whose claims do not match a live staff
// row, are 401. Refused requests change nothing.
func TestStaffScopeMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	other := newOtherApp(t, w)
	staffB, sessB := boundStaff(t, w, "owner-b", other.app, otherChannelID, "Ufixturestaffb00000000000000001", "Bo", "manager")
	memberA, appA := uuid.UUID(f.member.Member.Id), uuid.UUID(f.app.Id)
	foreign := append(other.markers(w), "Bo", staffB.Id.String())

	// Staff A's own operations.
	for _, op := range staffOps {
		t.Run(op.id+"/own", func(t *testing.T) {
			f.scan = f.scanAs(f.staffSess.SessionToken, f.member)
			r := f.send(op, f.staffSess.SessionToken, op.body, uuid.NewString())
			want(t, r, http.StatusOK)
			leaks(t, r.body, foreign)
			var got struct {
				Id       uuid.UUID `json:"id"`
				MemberId uuid.UUID `json:"member_id"`
				Member   struct {
					Id uuid.UUID `json:"id"`
				} `json:"member"`
			}
			r.into(t, &got)
			switch op.id {
			case "getStaffMe":
				if got.Id != uuid.UUID(f.staff.Id) {
					t.Errorf("staff me = %s, want %s", got.Id, f.staff.Id)
				}
			case "staffScan":
				if got.Member.Id != memberA {
					t.Errorf("scanned member = %s", got.Member.Id)
				}
			default:
				if got.MemberId != memberA {
					t.Errorf("transaction for member %s", got.MemberId)
				}
			}
		})
	}

	beforeA, beforeB := w.snapshot(w.tenantA), w.snapshot(w.tenantB)
	tokA := f.scanAs(f.staffSess.SessionToken, f.member)
	crossCases := []struct {
		name   string
		op     string
		token  string
		scan   string
		status int
		code   string
	}{
		// The scan itself: app B's staff reading app A's card.
		{"staff B scans member A's card", "staffScan", sessB.SessionToken, "", http.StatusUnprocessableEntity, "invalid_card"},
		// Holding app A's scan token (stolen or replayed).
		{"staff B earns with staff A's scan token", "staffEarnPoints", sessB.SessionToken, tokA, http.StatusForbidden, "scan_mismatch"},
		{"staff B redeems with staff A's scan token", "staffRedeemPoints", sessB.SessionToken, tokA, http.StatusForbidden, "scan_mismatch"},
		// A token naming staff B and app A cannot be used from app B.
		{"staff B, token claiming app A", "staffRedeemPoints", sessB.SessionToken, mustScanToken(t, w, uuid.UUID(staffB.Id), memberA, appA, w.tenantA), http.StatusForbidden, "scan_mismatch"},
		// A token naming app B and member A: member A is not in app B.
		{"staff B, token naming member A in app B", "staffRedeemPoints", sessB.SessionToken, mustScanToken(t, w, uuid.UUID(staffB.Id), memberA, uuid.UUID(other.app.Id), w.tenantB), http.StatusNotFound, "not_found"},
		{"staff B earns, token naming member A in app B", "staffEarnPoints", sessB.SessionToken, mustScanToken(t, w, uuid.UUID(staffB.Id), memberA, uuid.UUID(other.app.Id), w.tenantB), http.StatusNotFound, "not_found"},
		// Two staff members of one app: the scan is bound to the scanner.
		{"cashier redeems with the manager's scan token", "staffRedeemPoints", f.cashierSess.SessionToken, tokA, http.StatusForbidden, "scan_mismatch"},
		{"cashier earns with the manager's scan token", "staffEarnPoints", f.cashierSess.SessionToken, tokA, http.StatusForbidden, "scan_mismatch"},
	}
	for _, c := range crossCases {
		t.Run(c.name, func(t *testing.T) {
			op := staffOpByID(t, c.op)
			f.scan = c.scan
			r := f.send(op, c.token, op.body, uuid.NewString())
			if r.code != c.status {
				t.Fatalf("status = %d, want %d; body %s", r.code, c.status, r.body)
			}
			if code := r.errCode(t); code != c.code {
				t.Errorf("code = %s, want %s", code, c.code)
			}
			leaks(t, r.body, append(f.markers(), fixtureLineName, memberA.String()))
		})
	}
	// Staff B's own profile names nothing of app A.
	me := w.call(http.MethodGet, "/v1/staff/me", sessB.SessionToken, "", "", nil)
	want(t, me, http.StatusOK)
	leaks(t, me.body, f.markers())
	assertUnchanged(t, beforeA, w.snapshot(w.tenantA), "tenant A after cross-app staff calls")
	assertUnchanged(t, beforeB, w.snapshot(w.tenantB), "tenant B after cross-app staff calls")

	// Credentials that are not a live staff session of app A.
	now := time.Now()
	staffA := uuid.UUID(f.staff.Id)
	refused := map[string]struct{ token, apiKey string }{
		"dashboard token":                {"owner-a", ""},
		"member token":                   {f.member.SessionToken, ""},
		"api key":                        {"", f.key.Key},
		"api key and staff token":        {f.staffSess.SessionToken, f.key.Key},
		"no credentials":                 {"", ""},
		"garbage":                        {"not-a-token", ""},
		"member-typed JWT, staff A":      {forgedStaffToken(t, staffA, w.tenantA, appA, "member", now, time.Hour), ""},
		"dashboard-typed JWT, staff A":   {forgedStaffToken(t, staffA, w.tenantA, appA, "dashboard", now, time.Hour), ""},
		"staff A claims tenant B app B":  {forgedStaffToken(t, staffA, w.tenantB, uuid.UUID(other.app.Id), "staff", now, time.Hour), ""},
		"staff A claims tenant A app B":  {forgedStaffToken(t, staffA, w.tenantA, uuid.UUID(other.app.Id), "staff", now, time.Hour), ""},
		"staff B claims tenant A app A":  {forgedStaffToken(t, uuid.UUID(staffB.Id), w.tenantA, appA, "staff", now, time.Hour), ""},
		"unknown staff, right app":       {forgedStaffToken(t, uuid.New(), w.tenantA, appA, "staff", now, time.Hour), ""},
		"expired session":                {forgedStaffToken(t, staffA, w.tenantA, appA, "staff", now.Add(-13*time.Hour), 12*time.Hour), ""},
		"session from the future":        {forgedStaffToken(t, staffA, w.tenantA, appA, "staff", now.Add(time.Hour), 12*time.Hour), ""},
		"member session of a staff's ID": {forgedMemberToken(t, staffA, w.tenantA, appA, "member", now, time.Hour), ""},
	}
	beforeA, beforeB = w.snapshot(w.tenantA), w.snapshot(w.tenantB)
	for _, op := range staffOps {
		for name, c := range refused {
			t.Run(op.id+"/"+name, func(t *testing.T) {
				f.scan = tokA
				key := ""
				if op.idem {
					key = uuid.NewString()
				}
				r := w.call(op.method, op.path(f), c.token, c.apiKey, key, bodyOf(f, op.body))
				if r.code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401; body %s", r.code, r.body)
				}
				if code := r.errCode(t); code != "unauthorized" {
					t.Errorf("code = %s, want unauthorized", code)
				}
				leaks(t, r.body, append(f.markers(), other.markers(w)...))
			})
		}
	}
	assertUnchangedExcept(t, beforeA, w.snapshot(w.tenantA), "tenant A after refused staff calls", "api_keys")
	assertUnchanged(t, beforeB, w.snapshot(w.tenantB), "tenant B after refused staff calls")
}

func staffOpByID(t *testing.T, id string) opCase {
	t.Helper()
	for _, op := range staffOps {
		if op.id == id {
			return op
		}
	}
	t.Fatalf("no staff op %s", id)
	return opCase{}
}

func mustScanToken(t *testing.T, w *world, staffID, memberID, appID, tenantID uuid.UUID) string {
	t.Helper()
	tok, _, err := w.cards.IssueScanToken(staffID, memberID, appID, tenantID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// Invariant: a staff session ends when the staff member is disabled,
// deleted, unlinked from LINE or given a new invite code. A session and a
// scan token issued before that are refused on every staff route, and the
// member's ledger does not change.
func TestEndedStaffSessionsAreRefused(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	memberA := uuid.UUID(f.member.Member.Id)
	owner := w.as("owner-a")

	cases := []struct {
		name string
		end  func(st api.StaffCreated)
	}{
		{"disabled", func(st api.StaffCreated) {
			want(t, owner.patch("/v1/dashboard/staff/"+st.Id.String(), uuid.NewString(), map[string]any{"status": "disabled"}), http.StatusOK)
		}},
		{"deleted", func(st api.StaffCreated) {
			want(t, owner.del("/v1/dashboard/staff/"+st.Id.String()), http.StatusNoContent)
		}},
		{"invite reset", func(st api.StaffCreated) {
			want(t, owner.post("/v1/dashboard/staff/"+st.Id.String()+"/invite", uuid.NewString(), nil), http.StatusOK)
		}},
		// No API unlinks without a reset; the row is changed directly to
		// prove the session check reads the binding.
		{"unbound", func(st api.StaffCreated) {
			w.db.Exec(t, "UPDATE staff SET line_user_id = NULL WHERE id = $1", uuid.UUID(st.Id))
		}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, sess := boundStaff(t, w, "owner-a", f.app, fixtureChannelID, "Uendedstaff"+string(rune('a'+i))+"0000000000000000000", "Ended "+c.name, "manager")
			scan := f.scanAs(sess.SessionToken, f.member)
			before := w.ledgerRows(memberA)
			c.end(st)
			for _, op := range staffOps {
				f.scan = scan
				r := f.send(op, sess.SessionToken, op.body, uuid.NewString())
				if r.code != http.StatusUnauthorized {
					t.Errorf("%s: status = %d, want 401; body %s", op.id, r.code, r.body)
				}
			}
			if got := w.ledgerRows(memberA); got != before {
				t.Errorf("ledger rows %d -> %d", before, got)
			}
		})
	}
}

// Invariant: staff earn and redeem run once per Idempotency-Key. A replay
// returns the identical response and adds no row; another payload is 422;
// no key is 400. The key is scoped to the staff member, so another staff
// member of the app reusing it runs their own write instead of seeing 422
// or the first outcome. Authorization runs before a replay: once the
// staff session ends, the replay is 401.
func TestStaffIdempotencyMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)

	for _, op := range staffOps {
		if !op.idem {
			continue
		}
		t.Run(op.id, func(t *testing.T) {
			before := w.counts(w.tenantA)
			key := uuid.NewString()
			f.scan = f.scanAs(f.staffSess.SessionToken, f.member)
			managerScan := f.scan

			first := f.send(op, f.staffSess.SessionToken, op.body, key)
			if first.code != op.created {
				t.Fatalf("first: status = %d; body %s", first.code, first.body)
			}
			replay := f.send(op, f.staffSess.SessionToken, op.body, key)
			if replay.code != op.created || !bytes.Equal(first.body, replay.body) {
				t.Fatalf("replay: status = %d; bodies\n%s\n%s", replay.code, first.body, replay.body)
			}
			mismatch := f.mismatch(t, op, f.staffSess.SessionToken, key)
			if mismatch.code != http.StatusUnprocessableEntity || mismatch.errCode(t) != "idempotency_mismatch" {
				t.Fatalf("mismatch: status = %d; body %s", mismatch.code, mismatch.body)
			}
			if r := w.call(op.method, op.path(f), f.staffSess.SessionToken, "", "", bodyOf(f, op.body)); r.code != http.StatusBadRequest {
				t.Fatalf("no key: status = %d, want 400; body %s", r.code, r.body)
			}

			f.scan = f.scanAs(f.cashierSess.SessionToken, f.member)
			otherRun := f.send(op, f.cashierSess.SessionToken, op.body, key)
			if otherRun.code != op.created || bytes.Equal(otherRun.body, first.body) {
				t.Fatalf("cashier with the same key: status = %d; body %s", otherRun.code, otherRun.body)
			}

			after := w.counts(w.tenantA)
			for _, tbl := range tenantTables {
				wantDelta := 0
				if tbl.name == "idempotency_keys" || tbl.name == op.creates || slices.Contains(op.more, tbl.name) {
					wantDelta = 2
				}
				if got := after[tbl.name] - before[tbl.name]; got != wantDelta {
					t.Errorf("%s: %d new rows, want %d", tbl.name, got, wantDelta)
				}
			}
			f.scan = managerScan
		})
	}

	// The manager's replays after their invite is reset: refused, not served.
	f.scan = f.scanAs(f.staffSess.SessionToken, f.member)
	keys := map[string]string{}
	for _, op := range staffOps {
		if op.idem {
			keys[op.id] = uuid.NewString()
			want(t, f.send(op, f.staffSess.SessionToken, op.body, keys[op.id]), http.StatusOK)
		}
	}
	want(t, w.as("owner-a").post("/v1/dashboard/staff/"+f.staff.Id.String()+"/invite", uuid.NewString(), nil), http.StatusOK)
	for _, op := range staffOps {
		if op.idem {
			if r := f.send(op, f.staffSess.SessionToken, op.body, keys[op.id]); r.code != http.StatusUnauthorized {
				t.Errorf("%s replay after the invite reset: status = %d, want 401; body %s", op.id, r.code, r.body)
			}
		}
	}
}
