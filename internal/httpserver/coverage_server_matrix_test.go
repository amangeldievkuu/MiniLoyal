package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

// serverKeys are API keys of app A and app B by scope.
type serverKeys struct {
	a, b map[string]api.ApiKeyCreated
}

func mintKey(t *testing.T, w *world, token string, app api.App, scopes ...string) api.ApiKeyCreated {
	t.Helper()
	r := w.as(token).post("/v1/dashboard/apps/"+app.Id.String()+"/api-keys", uuid.NewString(), map[string]any{"name": "k", "scopes": scopes})
	want(t, r, http.StatusCreated)
	var k api.ApiKeyCreated
	r.into(t, &k)
	return k
}

func newServerKeys(t *testing.T, w *world, f *matrixFixtures, other otherApp) serverKeys {
	t.Helper()
	k := serverKeys{
		a: map[string]api.ApiKeyCreated{
			"members:read":  f.key,
			"points:write":  mintKey(t, w, "owner-a", f.app, "points:write"),
			"coupons:write": mintKey(t, w, "owner-a", f.app, "coupons:write"),
		},
		b: map[string]api.ApiKeyCreated{},
	}
	for _, scope := range []string{"members:read", "points:write", "coupons:write"} {
		k.b[scope] = mintKey(t, w, "owner-b", other.app, scope)
	}
	return k
}

// otherScope is a scope the operation does not accept.
func otherScope(scope string) string {
	if scope == "members:read" {
		return "points:write"
	}
	return "members:read"
}

func (f *matrixFixtures) serverCall(op opCase, token, apiKey, idemKey string) tresp {
	if !op.idem {
		idemKey = ""
	}
	return f.w.call(op.method, op.path(f), token, apiKey, idemKey, bodyOf(f, op.body))
}

// Invariant: every server route needs an API key with its scope. The key
// sees only its own app's members: a key of another app gets 404 for a
// foreign member (and a list without them), and its writes change nothing;
// dashboard, member and staff tokens and missing credentials are 401; a key
// with the wrong scope is 403. Refused requests change nothing.
func TestServerMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	other := newOtherApp(t, w)
	keys := newServerKeys(t, w, f, other)
	beforeA, beforeB := w.snapshot(w.tenantA), w.snapshot(w.tenantB)

	for _, op := range serverOps {
		t.Run(op.id+"/key without scope", func(t *testing.T) {
			r := f.serverCall(op, "", keys.a[otherScope(op.scope)].Key, uuid.NewString())
			want(t, r, http.StatusForbidden)
			if code := r.errCode(t); code != "forbidden" {
				t.Errorf("code = %s", code)
			}
			leaks(t, r.body, f.markers())
		})
		t.Run(op.id+"/key of another app", func(t *testing.T) {
			r := f.serverCall(op, "", keys.b[op.scope].Key, uuid.NewString())
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
			"staff token":         {f.staffSess.SessionToken, ""},
			"no credentials":      {"", ""},
			"unknown key":         {"", "ml_" + strings.Repeat("x", 40)},
			"key in bearer":       {keys.a[op.scope].Key, ""},
		}
		for name, c := range refused {
			t.Run(op.id+"/"+name, func(t *testing.T) {
				r := f.serverCall(op, c.token, c.apiKey, uuid.NewString())
				want(t, r, http.StatusUnauthorized)
				if code := r.errCode(t); code != "unauthorized" {
					t.Errorf("code = %s", code)
				}
				leaks(t, r.body, f.markers())
			})
		}
	}
	// Successful key authentication touches api_keys.last_used_at
	// asynchronously; nothing else may change.
	assertUnchangedExcept(t, beforeA, w.snapshot(w.tenantA), "tenant A after refused server calls", "api_keys")
	assertUnchangedExcept(t, beforeB, w.snapshot(w.tenantB), "tenant B after refused server calls", "api_keys")

	for _, op := range serverOps {
		t.Run(op.id+"/own key", func(t *testing.T) {
			r := f.serverCall(op, "", keys.a[op.scope].Key, uuid.NewString())
			want(t, r, op.okStatus())
			leaks(t, r.body, other.markers(w))
			if !bytes.Contains(r.body, []byte(f.member.Member.Id.String())) {
				t.Errorf("own member missing: %s", r.body)
			}
		})
	}

	// A member erased by the dashboard disappears from the server API too,
	// for reads and for points writes.
	want(t, w.as("owner-a").del("/v1/dashboard/members/"+f.member.Member.Id.String()), http.StatusNoContent)
	for _, op := range serverOps {
		if op.id == "serverListMembers" {
			list := f.serverCall(op, "", f.key.Key, "")
			want(t, list, http.StatusOK)
			if bytes.Contains(list.body, []byte(f.member.Member.Id.String())) || bytes.Contains(list.body, []byte(fixtureLineUser)) {
				t.Errorf("erased member still listed: %s", list.body)
			}
			continue
		}
		if r := f.serverCall(op, "", keys.a[op.scope].Key, uuid.NewString()); r.code != http.StatusNotFound {
			t.Errorf("%s on an erased member: status = %d, want 404; body %s", op.id, r.code, r.body)
		}
	}
}

// Invariant: a server write with an Idempotency-Key runs once. Replaying it
// returns the identical response and adds no row; another payload is 422;
// no key is 400; the key is scoped to the API key, so a second key of the
// same app runs its own write; and a revoked key's replay is refused.
func TestServerIdempotencyMatrix(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	other := newOtherApp(t, w)
	keys := newServerKeys(t, w, f, other)

	for _, op := range serverOps {
		if !op.idem {
			continue
		}
		t.Run(op.id, func(t *testing.T) {
			key := keys.a[op.scope].Key
			idemKey := uuid.NewString()
			second := mintKey(t, w, "owner-a", f.app, op.scope)
			before := w.counts(w.tenantA)

			first := f.serverCall(op, "", key, idemKey)
			if first.code != op.created {
				t.Fatalf("first: status = %d, want %d; body %s", first.code, op.created, first.body)
			}
			replay := f.serverCall(op, "", key, idemKey)
			if replay.code != op.created || !bytes.Equal(first.body, replay.body) {
				t.Fatalf("replay: status = %d; bodies\n%s\n%s", replay.code, first.body, replay.body)
			}
			mismatch := w.call(op.method, op.path(f), "", key, idemKey, op.alt(f))
			if mismatch.code != http.StatusUnprocessableEntity || mismatch.errCode(t) != "idempotency_mismatch" {
				t.Fatalf("mismatch: status = %d; body %s", mismatch.code, mismatch.body)
			}
			if r := f.serverCall(op, "", key, ""); r.code != http.StatusBadRequest {
				t.Fatalf("no key: status = %d, want 400; body %s", r.code, r.body)
			}
			otherRun := f.serverCall(op, "", second.Key, idemKey)
			if otherRun.code != op.created || bytes.Equal(otherRun.body, first.body) {
				t.Fatalf("second key with the same Idempotency-Key: status = %d; body %s", otherRun.code, otherRun.body)
			}

			after := w.counts(w.tenantA)
			for _, tbl := range tenantTables {
				wantDelta := 0
				switch {
				case tbl.name == "idempotency_keys", tbl.name == op.creates, slices.Contains(op.more, tbl.name):
					wantDelta = 2
				}
				if got := after[tbl.name] - before[tbl.name]; got != wantDelta {
					t.Errorf("%s: %d new rows, want %d", tbl.name, got, wantDelta)
				}
			}

			// Authorization runs before a replay: a revoked key gets 401, not
			// the stored outcome.
			want(t, w.as("owner-a").del("/v1/dashboard/api-keys/"+second.Id.String()), http.StatusNoContent)
			if r := f.serverCall(op, "", second.Key, idemKey); r.code != http.StatusUnauthorized {
				t.Errorf("revoked key replay: status = %d, want 401; body %s", r.code, r.body)
			}
			if op.id != "serverPostPoints" {
				return
			}
			var ledger int
			if err := w.db.Admin.QueryRow(context.Background(), "SELECT count(*) FROM points_ledger WHERE ref_id = 'R-1'").Scan(&ledger); err != nil {
				t.Fatal(err)
			}
			if ledger != 2 {
				t.Errorf("ledger rows for the receipt = %d, want 2 (one per API key)", ledger)
			}
		})
	}
}
