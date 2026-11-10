package points_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

func (e *env) apiKey() principal.Principal {
	return principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: e.tenant, AppID: e.app, Scopes: []string{"points:write"}}
}

// Phase 4 re-review: API key points writes are audited as points.<kind>
// with the key as actor, the request ID and the IP. The row holds the
// transaction, kind, points and ref_type, never the note or ref_id. A replay
// writes no second row and a refused write rolls its row back.
func TestServerPostsAreAuditedWithIDsOnly(t *testing.T) {
	e := newEnv(t)
	ctx := audit.WithRequestInfo(context.Background(), audit.RequestInfo{RequestID: "req-srv", IP: "203.0.113.7"})
	e.rules(t, nil)
	m := e.member(t, e.tenant, e.app)
	key := e.apiKey()

	earnIn := func() points.ServerInput {
		return points.ServerInput{Type: points.KindEarn, Points: intp(20), RefType: strp("receipt"), RefID: strp("R-0912"), Note: strp("Mr Lin 0912-345-678")}
	}
	earnKey := idem()
	earned, err := e.svc.ServerPost(ctx, key, m, earnKey, earnIn())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ServerPost(ctx, key, m, earnKey, earnIn()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	redeemed, err := e.svc.ServerPost(ctx, key, m, idem(), points.ServerInput{Type: points.KindRedeem, Points: intp(5)})
	if err != nil {
		t.Fatal(err)
	}
	adjusted, err := e.svc.ServerPost(ctx, key, m, idem(), points.ServerInput{Type: points.KindAdjust, Points: intp(-2), Note: strp("Mr Lin asked")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ServerPost(ctx, key, m, idem(), points.ServerInput{Type: points.KindRedeem, Points: intp(1000)}); code(err) != "insufficient_points" {
		t.Fatalf("overdraw: %v", err)
	}

	for _, c := range []struct {
		action string
		txn    points.Txn
		want   []string
	}{
		{"points.earn", earned, []string{`"kind": "earn"`, `"points": 20`, `"ref_type": "receipt"`}},
		{"points.redeem", redeemed, []string{`"kind": "redeem"`, `"points": -5`}},
		{"points.adjust", adjusted, []string{`"kind": "adjust"`, `"points": -2`}},
	} {
		rows, err := e.d.Admin.Query(ctx, "SELECT actor_type, actor_id::text, request_id, host(ip), metadata::text FROM audit_log WHERE action = $1 AND target_id = $2",
			c.action, m.String())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			n++
			var actorType, actorID, requestID, ip, meta string
			if err := rows.Scan(&actorType, &actorID, &requestID, &ip, &meta); err != nil {
				t.Fatal(err)
			}
			if actorType != "api_key" || actorID != key.APIKeyID.String() || requestID != "req-srv" || ip != "203.0.113.7" {
				t.Errorf("%s actor and request: %s %s %s %s", c.action, actorType, actorID, requestID, ip)
			}
			for _, want := range append(c.want, `"txn_id": "`+c.txn.ID.String()) {
				if !strings.Contains(meta, want) {
					t.Errorf("%s metadata %s lacks %s", c.action, meta, want)
				}
			}
			for _, banned := range []string{"note", "ref_id", "0912", "Lin"} {
				if strings.Contains(meta, banned) {
					t.Errorf("%s metadata holds %q: %s", c.action, banned, meta)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: %d audit rows, want 1", c.action, n)
		}
	}
}

// API key writes are limited per key by one limiter that coupon issues
// share: over the limit the answer is 429 rate_limited with a retry time,
// another key is not affected, and the bucket refills with the clock.
func TestServerPostsAreRateLimitedPerKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyring := crypto.Static(bytes.Repeat([]byte{5}, 32))
	shared := access.NewAPIKeyLimiter(e.clk, 3)
	svc := points.New(points.Deps{
		Pool: e.d.App, Tenants: tenant.New(e.d.App, keyring, e.clk, log), Keyring: keyring, Card: e.codec, Clock: e.clk, Log: log,
		StaffWritesPerMinute: -1, APIKeyLimiter: shared,
	})
	m := e.member(t, e.tenant, e.app)
	key, other := e.apiKey(), e.apiKey()
	earn := points.ServerInput{Type: points.KindEarn, Points: intp(1)}

	// A coupon issue by the same key spends from the same bucket.
	if err := access.AllowAPIKey(shared, key); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := svc.ServerPost(ctx, key, m, idem(), earn); err != nil {
			t.Fatalf("post %d: %v", i+1, err)
		}
	}
	_, err := svc.ServerPost(ctx, key, m, idem(), earn)
	ae, ok := apierr.As(err)
	if !ok || ae.Code != "rate_limited" || ae.Status != 429 || ae.Details["retry_after_seconds"] == nil {
		t.Fatalf("fourth write in a minute: %v, want 429 rate_limited with retry_after_seconds", err)
	}
	if _, err := svc.ServerPost(ctx, other, m, idem(), earn); err != nil {
		t.Errorf("another key: %v", err)
	}
	e.clk.Advance(30 * time.Second)
	if _, err := svc.ServerPost(ctx, key, m, idem(), earn); err != nil {
		t.Errorf("after the bucket refilled: %v", err)
	}
}
