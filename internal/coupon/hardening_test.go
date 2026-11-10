package coupon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Phase 4 security review follow-ups (docs/security/phase4-review.md).

// Invariant: one scan redeems at most MaxRedeemsPerScan coupons; the next
// redeem on it is 409 scan_used and leaves the coupon issued. A refused
// redeem does not use one up, and a fresh scan redeems again.
func TestOneScanRedeemsAtMostThreeCoupons(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{})
	coupons := make([]Instance, MaxRedeemsPerScan+2)
	for i := range coupons {
		coupons[i] = e.issue(t, m, tmpl.ID)
	}
	st := e.staff()
	scan := e.scan(t, st, m)

	// A refused redeem (an unknown coupon) does not count.
	if _, err := e.svc.Redeem(ctx, st, uuid.New(), idem(), scan); code(err) != "coupon_unavailable" {
		t.Fatalf("unknown coupon: %v", err)
	}
	for i := range MaxRedeemsPerScan {
		if _, err := e.svc.Redeem(ctx, st, coupons[i].ID, idem(), scan); err != nil {
			t.Fatalf("redeem %d on one scan: %v", i+1, err)
		}
	}
	_, err := e.svc.Redeem(ctx, st, coupons[MaxRedeemsPerScan].ID, idem(), scan)
	if code(err) != "scan_used" || statusOf(err) != 409 {
		t.Fatalf("redeem %d on one scan: %v, want 409 scan_used", MaxRedeemsPerScan+1, err)
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND status = 'redeemed'", m); got != MaxRedeemsPerScan {
		t.Errorf("redeemed = %d, want %d", got, MaxRedeemsPerScan)
	}
	if _, err := e.svc.Redeem(ctx, st, coupons[MaxRedeemsPerScan].ID, idem(), e.scan(t, st, m)); err != nil {
		t.Errorf("fresh scan: %v", err)
	}
}

// Invariant: concurrent redeems of different coupons on one scan redeem
// exactly MaxRedeemsPerScan; every other attempt is scan_used.
func TestConcurrentRedeemsOnOneScanStopAtThree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{})
	const n = 12
	coupons := make([]Instance, n)
	for i := range coupons {
		coupons[i] = e.issue(t, m, tmpl.ID)
	}
	st := e.staff()
	scan := e.scan(t, st, m)
	codes := make([]string, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			_, err := e.svc.Redeem(context.Background(), st, coupons[i].ID, idem(), scan)
			codes[i] = code(err)
		}()
	}
	close(release)
	wg.Wait()
	ok, used := 0, 0
	for i, c := range codes {
		switch c {
		case "":
			ok++
		case "scan_used":
			used++
		default:
			t.Fatalf("redeem %d: %s", i, c)
		}
	}
	if ok != MaxRedeemsPerScan || used != n-MaxRedeemsPerScan {
		t.Errorf("%d redeemed and %d scan_used, want %d and %d", ok, used, MaxRedeemsPerScan, n-MaxRedeemsPerScan)
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND status = 'redeemed'", m); got != MaxRedeemsPerScan {
		t.Errorf("redeemed rows = %d, want %d", got, MaxRedeemsPerScan)
	}
	if got := e.count(t, "SELECT redeems FROM scan_coupon_redeems"); got != MaxRedeemsPerScan {
		t.Errorf("recorded redeems = %d, want %d", got, MaxRedeemsPerScan)
	}
}

// The maintenance sweep drops scan redeem counts older than a day.
func TestDeleteOldScanRedeems(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
	e.d.Exec(t, "INSERT INTO scan_coupon_redeems (jti, tenant_id, app_id, redeems, used_at) VALUES ('old', $1, $2, 1, $3), ('new', $1, $2, 2, $4)",
		e.tenant, e.app, now.Add(-25*time.Hour), now.Add(-time.Hour))
	n, err := DeleteOldScanRedeems(context.Background(), e.d.App, now)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v; want 1", n, err)
	}
	if got := e.count(t, "SELECT count(*) FROM scan_coupon_redeems WHERE jti = 'new'"); got != 1 {
		t.Errorf("recent row deleted")
	}
}

// rateEnv is newEnv with the claim and API issue limits switched on.
func rateEnv(t *testing.T, claims, issues int) *env {
	e := newEnv(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyring := crypto.Static(bytes.Repeat([]byte{5}, 32))
	e.svc = New(Deps{
		Pool: e.d.App, Tenants: tenant.New(e.d.App, keyring, e.clk, log), Keyring: keyring, Card: e.codec, Clock: e.clk, Log: log,
		StaffWritesPerMinute: -1, ClaimsPerMinute: claims, APIIssuesPerMinute: issues,
	})
	return e
}

// Claims are limited per member and API issues per key: over the limit the
// answer is 429 rate_limited with a retry time, other members and keys are
// not affected, and the bucket refills with the clock.
func TestClaimAndAPIIssueRateLimits(t *testing.T) {
	e := rateEnv(t, 2, 3)
	ctx := context.Background()
	tmpl := e.template(t, TemplateInput{Claimable: boolp(true), PerMemberLimit: intp(100)})
	m, other := e.member(t), e.member(t)

	for i := range 2 {
		if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem()); err != nil {
			t.Fatalf("claim %d: %v", i+1, err)
		}
	}
	_, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem())
	if code(err) != "rate_limited" || statusOf(err) != 429 || detailsOf(err)["retry_after_seconds"] == nil {
		t.Fatalf("third claim in a minute: %v, want 429 rate_limited with retry_after_seconds", err)
	}
	if _, err := e.svc.Claim(ctx, e.memberP(other), tmpl.ID, idem()); err != nil {
		t.Errorf("another member's claim: %v", err)
	}
	e.clk.Advance(30 * time.Second)
	if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem()); err != nil {
		t.Errorf("claim after the bucket refilled: %v", err)
	}

	key, otherKey := e.apiKey(e.tenant, e.app), e.apiKey(e.tenant, e.app)
	for i := range 3 {
		if _, err := e.svc.Issue(ctx, key, m, tmpl.ID, idem()); err != nil {
			t.Fatalf("API issue %d: %v", i+1, err)
		}
	}
	if _, err := e.svc.Issue(ctx, key, m, tmpl.ID, idem()); code(err) != "rate_limited" || statusOf(err) != 429 {
		t.Fatalf("fourth API issue in a minute: %v, want 429 rate_limited", err)
	}
	if _, err := e.svc.Issue(ctx, otherKey, m, tmpl.ID, idem()); err != nil {
		t.Errorf("another key's issue: %v", err)
	}
	// Dashboard issues are audited by hand and not limited.
	for range 5 {
		e.issue(t, m, tmpl.ID)
	}
}

// API key issues are audited like dashboard issues, with the key as the
// actor; void audits keep IDs only, since the reason is personal text that
// an erasure blanks on the coupon.
func TestAPIIssuesAndVoidsAreAuditedWithIDsOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{})
	key := e.apiKey(e.tenant, e.app)
	inst, err := e.svc.Issue(ctx, key, m, tmpl.ID, idem())
	if err != nil {
		t.Fatal(err)
	}
	var actorType, actorID, meta string
	if err := e.d.Admin.QueryRow(ctx, "SELECT actor_type, actor_id::text, metadata::text FROM audit_log WHERE action = 'coupon.issue' AND metadata->>'coupon_id' = $1",
		inst.ID.String()).Scan(&actorType, &actorID, &meta); err != nil {
		t.Fatalf("API issue audit row: %v", err)
	}
	if actorType != "api_key" || actorID != key.APIKeyID.String() || !strings.Contains(meta, tmpl.ID.String()) {
		t.Errorf("API issue audit: %s %s %s", actorType, actorID, meta)
	}

	if _, err := e.svc.Void(ctx, e.owner, inst.ID, idem(), "member Wang called from 0912-000-111"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Admin.QueryRow(ctx, "SELECT metadata::text FROM audit_log WHERE action = 'coupon.void' AND metadata->>'coupon_id' = $1", inst.ID.String()).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta, "Wang") || strings.Contains(meta, "reason") {
		t.Errorf("void audit holds the reason: %s", meta)
	}
}
