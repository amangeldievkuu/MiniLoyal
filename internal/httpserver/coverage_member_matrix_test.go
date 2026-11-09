package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
)

// The member and server matrices: every memberOp and serverOp from
// coverage_matrix_test.go, run with every credential kind that must be
// refused and with the one that must succeed, against two apps in two
// tenants so a leak across apps shows up as a marker in a response body.

// Tenant B's app, channel and LINE member.
const (
	otherChannelID = "2007000009"
	otherLineUser  = "Ufixtureb0000000000000000000002"
	otherLineName  = "Bao-Yu"
)

// otherApp is tenant B's app with one signed-in member.
type otherApp struct {
	app    api.App
	member api.MemberSession
}

func newOtherApp(t *testing.T, w *world) otherApp {
	t.Helper()
	app := w.createApp("owner-b", w.tenantB, "Beta Bistro")
	w.putChannel("owner-b", app, otherChannelID)
	return otherApp{app: app, member: w.lineLogin(app, otherChannelID, otherLineUser, otherLineName)}
}

// markers are strings of tenant B's data that must never appear in a
// response to tenant A's principals, and vice versa.
func (o otherApp) markers(w *world) []string {
	return []string{otherLineName, otherLineUser, o.member.Member.Id.String(), o.member.SessionToken, o.app.Id.String(), w.tenantB.String(), "Beta Bistro"}
}

// forgedMemberToken signs a member session with the server's own key. Every
// claim is right except the ones the case names, so a 401 proves the server
// checks the member row against the token, not just the signature.
func forgedMemberToken(t *testing.T, sub, tenantID, appID uuid.UUID, typ string, iat time.Time, ttl time.Duration) string {
	t.Helper()
	tok, err := jwt.NewBuilder().
		Issuer(lineauth.DefaultIssuer).
		Subject(sub.String()).
		IssuedAt(iat).
		Expiration(iat.Add(ttl)).
		Claim("typ", typ).
		Claim("tenant_id", tenantID.String()).
		Claim("app_id", appID.String()).
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

func leaks(t *testing.T, body []byte, markers []string) {
	t.Helper()
	scanned := withoutRequestID(body)
	for _, m := range markers {
		if bytes.Contains(scanned, []byte(m)) {
			t.Errorf("body leaks %q: %s", m, body)
		}
	}
}

// Invariant: a member session reaches only that member's own row in its own
// app. Dashboard tokens, API keys, missing credentials, sessions whose
// claims point at another app or tenant, and sessions of erased members are
// all refused with 401, and refused requests change nothing.
func TestMemberScopeMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	other := newOtherApp(t, w)
	a, b := f.member, other.member

	// Each member's own operations succeed and show only their own data.
	for _, op := range memberOps {
		if op.id == "deleteMe" {
			continue
		}
		for _, side := range []struct {
			name    string
			sess    api.MemberSession
			app     api.App
			foreign []string
		}{
			{"member-a", a, f.app, other.markers(w)},
			{"member-b", b, other.app, f.markers()},
		} {
			t.Run(op.id+"/"+side.name, func(t *testing.T) {
				r := f.send(op, side.sess.SessionToken, op.body, uuid.NewString())
				if r.code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body %s", r.code, r.body)
				}
				leaks(t, r.body, side.foreign)
				var got struct {
					Id     uuid.UUID `json:"id"`
					AppId  uuid.UUID `json:"app_id"`
					Member *struct {
						Id         uuid.UUID `json:"id"`
						AppId      uuid.UUID `json:"app_id"`
						LineUserId string    `json:"line_user_id"`
					} `json:"member"`
				}
				r.into(t, &got)
				if got.Member != nil { // exportMe wraps the member
					got.Id, got.AppId = got.Member.Id, got.Member.AppId
					if got.Member.LineUserId == "" {
						t.Error("export omits the member's own LINE user ID")
					}
				}
				if got.Id != uuid.UUID(side.sess.Member.Id) || got.AppId != uuid.UUID(side.app.Id) {
					t.Errorf("response is for member %s of app %s, want %s of %s", got.Id, got.AppId, side.sess.Member.Id, side.app.Id)
				}
			})
		}
	}

	// Credentials that are not a live session of a member of the app.
	now := time.Now()
	memberA, memberB := uuid.UUID(a.Member.Id), uuid.UUID(b.Member.Id)
	appA, appB := uuid.UUID(f.app.Id), uuid.UUID(other.app.Id)
	refused := map[string]struct{ token, apiKey string }{
		"dashboard token":                {"owner-a", ""},
		"dashboard-typed JWT, same key":  {forgedMemberToken(t, w.ownerA, w.tenantA, appA, "dashboard", now, time.Hour), ""},
		"api key":                        {"", f.key.Key},
		"api key and member token":       {a.SessionToken, f.key.Key},
		"no credentials":                 {"", ""},
		"garbage":                        {"not-a-token", ""},
		"member A claims tenant B app B": {forgedMemberToken(t, memberA, w.tenantB, appB, "member", now, time.Hour), ""},
		"member A claims tenant A app B": {forgedMemberToken(t, memberA, w.tenantA, appB, "member", now, time.Hour), ""},
		"member B claims tenant A app A": {forgedMemberToken(t, memberB, w.tenantA, appA, "member", now, time.Hour), ""},
		"unknown member, right app":      {forgedMemberToken(t, uuid.New(), w.tenantA, appA, "member", now, time.Hour), ""},
		"expired session":                {forgedMemberToken(t, memberA, w.tenantA, appA, "member", now.Add(-2*time.Hour), time.Hour), ""},
		"session from the future":        {forgedMemberToken(t, memberA, w.tenantA, appA, "member", now.Add(time.Hour), time.Hour), ""},
	}
	beforeA, beforeB := w.snapshot(w.tenantA), w.snapshot(w.tenantB)
	for _, op := range memberOps {
		for name, c := range refused {
			t.Run(op.id+"/"+name, func(t *testing.T) {
				var body any
				if op.body != nil {
					body = op.body(f)
				}
				key := ""
				if op.idem {
					key = uuid.NewString()
				}
				r := w.call(op.method, op.path(f), c.token, c.apiKey, key, body)
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
	// The refused "api key" case still authenticates the key, which touches
	// api_keys.last_used_at asynchronously.
	assertUnchangedExcept(t, beforeA, w.snapshot(w.tenantA), "tenant A after refused member calls", "api_keys")
	assertUnchanged(t, beforeB, w.snapshot(w.tenantB), "tenant B after refused member calls")

	// A member who erased themselves, and one erased by the dashboard, hold
	// sessions that are still well signed and still refused everywhere.
	erasedByDashboard := w.lineLogin(f.app, fixtureChannelID, "Ufixturec0000000000000000000003", "Chien")
	want(t, w.as("owner-a").del("/v1/dashboard/members/"+erasedByDashboard.Member.Id.String()), http.StatusNoContent)
	r := f.send(memberOps[len(memberOps)-1], a.SessionToken, nil, "")
	want(t, r, http.StatusNoContent)
	var line string
	var anon bool
	if err := w.db.Admin.QueryRow(context.Background(), "SELECT line_user_id, anonymized_at IS NOT NULL FROM members WHERE id = $1", memberA).Scan(&line, &anon); err != nil {
		t.Fatal(err)
	}
	if !anon || !strings.HasPrefix(line, "erased:") {
		t.Errorf("member A after deleteMe: line_user_id=%q anonymized=%v", line, anon)
	}
	for _, op := range memberOps {
		for name, token := range map[string]string{"self-erased": a.SessionToken, "dashboard-erased": erasedByDashboard.SessionToken} {
			t.Run(op.id+"/"+name, func(t *testing.T) {
				r := f.send(op, token, op.body, uuid.NewString())
				if r.code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401; body %s", r.code, r.body)
				}
				if code := r.errCode(t); code != "unauthorized" {
					t.Errorf("code = %s, want unauthorized", code)
				}
			})
		}
	}
	// Member B is untouched by all of the above.
	ok := f.send(memberOps[0], b.SessionToken, nil, "")
	want(t, ok, http.StatusOK)
	leaks(t, ok.body, f.markers())
}

// Invariant: a member write with an Idempotency-Key runs once. Replaying
// the key with the same payload returns the identical response and adds no
// row; the same key with a different payload is 422; no key is 400; the
// key is scoped to the member, so another member's identical request runs
// on its own row; and a replay after erasure is refused, not served.
func TestMemberIdempotencyMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	other := newOtherApp(t, w)
	a, b := f.member, other.member

	consents := func(memberID api.MemberSession) int {
		t.Helper()
		var n int
		if err := w.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM member_consents WHERE member_id = $1", uuid.UUID(memberID.Member.Id)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	idemRows := func(tenantID uuid.UUID) int {
		t.Helper()
		var n int
		if err := w.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND scope LIKE 'member:%'", tenantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	keys := map[string]string{}
	for _, op := range memberOps {
		if !op.idem {
			continue
		}
		t.Run(op.id, func(t *testing.T) {
			key := uuid.NewString()
			keys[op.id] = key
			consentsA, consentsB := consents(a), consents(b)
			idemA, idemB := idemRows(w.tenantA), idemRows(w.tenantB)

			first := f.send(op, a.SessionToken, op.body, key)
			if first.code != op.created {
				t.Fatalf("first: status = %d, want %d; body %s", first.code, op.created, first.body)
			}
			replay := f.send(op, a.SessionToken, op.body, key)
			if replay.code != op.created {
				t.Fatalf("replay: status = %d, want %d; body %s", replay.code, op.created, replay.body)
			}
			if !bytes.Equal(first.body, replay.body) {
				t.Errorf("replay body differs:\n%s\n%s", first.body, replay.body)
			}
			mismatch := f.send(op, a.SessionToken, op.alt, key)
			if mismatch.code != http.StatusUnprocessableEntity {
				t.Fatalf("mismatch: status = %d, want 422; body %s", mismatch.code, mismatch.body)
			}
			if code := mismatch.errCode(t); code != "idempotency_mismatch" {
				t.Errorf("mismatch code = %s", code)
			}
			noKey := w.call(op.method, op.path(f), a.SessionToken, "", "", op.body(f))
			if noKey.code != http.StatusBadRequest {
				t.Fatalf("no key: status = %d, want 400; body %s", noKey.code, noKey.body)
			}
			// Member B reusing A's key runs B's own write and never sees A's
			// stored outcome.
			otherRun := f.send(op, b.SessionToken, op.body, key)
			if otherRun.code != op.created {
				t.Fatalf("member B with the same key: status = %d; body %s", otherRun.code, otherRun.body)
			}
			leaks(t, otherRun.body, []string{a.Member.Id.String(), fixtureLineUser})
			if !bytes.Contains(otherRun.body, []byte(b.Member.Id.String())) {
				t.Errorf("member B's response is not B's: %s", otherRun.body)
			}

			wantConsent := 0
			if op.creates == "member_consents" {
				wantConsent = 1
			}
			if got := consents(a) - consentsA; got != wantConsent {
				t.Errorf("member A gained %d consent rows, want %d", got, wantConsent)
			}
			if got := consents(b) - consentsB; got != wantConsent {
				t.Errorf("member B gained %d consent rows, want %d", got, wantConsent)
			}
			if got := idemRows(w.tenantA) - idemA; got != 1 {
				t.Errorf("tenant A gained %d idempotency rows, want 1", got)
			}
			if got := idemRows(w.tenantB) - idemB; got != 1 {
				t.Errorf("tenant B gained %d idempotency rows, want 1", got)
			}
		})
	}

	// Authorization runs before a replay: an erased member replaying a key
	// that once succeeded gets 401, not the stored 200.
	want(t, f.send(memberOps[len(memberOps)-1], a.SessionToken, nil, ""), http.StatusNoContent)
	for _, op := range memberOps {
		if !op.idem {
			continue
		}
		if r := f.send(op, a.SessionToken, op.body, keys[op.id]); r.code != http.StatusUnauthorized {
			t.Errorf("%s replay after erasure: status = %d, want 401; body %s", op.id, r.code, r.body)
		}
	}
}

// Invariant: the server member routes need an API key with members:read.
// The key sees only its own app's members; a key of another app gets 404
// for a foreign member and a list without it; dashboard and member tokens
// and missing credentials are 401; a key without the scope is 403.
func TestServerMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	other := newOtherApp(t, w)

	mint := func(token string, app api.App, scopes []string) api.ApiKeyCreated {
		t.Helper()
		r := w.as(token).post("/v1/dashboard/apps/"+app.Id.String()+"/api-keys", uuid.NewString(), map[string]any{"name": "k", "scopes": scopes})
		want(t, r, http.StatusCreated)
		var k api.ApiKeyCreated
		r.into(t, &k)
		return k
	}
	noScope := mint("owner-a", f.app, []string{"points:write"})
	keyB := mint("owner-b", other.app, []string{"members:read"})
	before := w.snapshot(w.tenantA)

	for _, op := range serverOps {
		t.Run(op.id+"/own key", func(t *testing.T) {
			r := w.call(op.method, op.path(f), "", f.key.Key, "", nil)
			want(t, r, http.StatusOK)
			leaks(t, r.body, other.markers(w))
			if !bytes.Contains(r.body, []byte(f.member.Member.Id.String())) {
				t.Errorf("own member missing: %s", r.body)
			}
		})
		t.Run(op.id+"/key without scope", func(t *testing.T) {
			r := w.call(op.method, op.path(f), "", noScope.Key, "", nil)
			want(t, r, http.StatusForbidden)
			if code := r.errCode(t); code != "forbidden" {
				t.Errorf("code = %s", code)
			}
			leaks(t, r.body, f.markers())
		})
		t.Run(op.id+"/key of another app", func(t *testing.T) {
			r := w.call(op.method, op.path(f), "", keyB.Key, "", nil)
			switch op.id {
			case "serverListMembers":
				want(t, r, http.StatusOK)
				if !bytes.Contains(r.body, []byte(other.member.Member.Id.String())) {
					t.Errorf("app B's key does not see app B's member: %s", r.body)
				}
			default:
				want(t, r, http.StatusNotFound)
				if code := r.errCode(t); code != "not_found" {
					t.Errorf("code = %s", code)
				}
			}
			leaks(t, r.body, f.markers())
		})
		refused := map[string]struct{ token, apiKey string }{
			"dashboard token":     {"owner-a", ""},
			"dashboard-typed JWT": {forgedMemberToken(t, w.ownerA, w.tenantA, uuid.UUID(f.app.Id), "dashboard", time.Now(), time.Hour), ""},
			"member token":        {f.member.SessionToken, ""},
			"no credentials":      {"", ""},
			"unknown key":         {"", "ml_" + strings.Repeat("x", 40)},
			"key in bearer":       {f.key.Key, ""},
		}
		for name, c := range refused {
			t.Run(op.id+"/"+name, func(t *testing.T) {
				r := w.call(op.method, op.path(f), c.token, c.apiKey, "", nil)
				want(t, r, http.StatusUnauthorized)
				if code := r.errCode(t); code != "unauthorized" {
					t.Errorf("code = %s", code)
				}
				leaks(t, r.body, f.markers())
			})
		}
	}

	// A member erased by the dashboard disappears from the server API too.
	want(t, w.as("owner-a").del("/v1/dashboard/members/"+f.member.Member.Id.String()), http.StatusNoContent)
	want(t, w.call(http.MethodGet, "/v1/members/"+f.member.Member.Id.String(), "", f.key.Key, "", nil), http.StatusNotFound)
	list := w.call(http.MethodGet, "/v1/members", "", f.key.Key, "", nil)
	want(t, list, http.StatusOK)
	if bytes.Contains(list.body, []byte(f.member.Member.Id.String())) || bytes.Contains(list.body, []byte(fixtureLineUser)) {
		t.Errorf("erased member still listed: %s", list.body)
	}

	// Reads changed nothing but the asynchronous api_keys.last_used_at
	// touch, and the deliberate erasure above touched members and audit_log.
	assertUnchangedExcept(t, before, w.snapshot(w.tenantA), "after server reads", "api_keys", "members", "audit_log")
}
