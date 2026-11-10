package points_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Security review Phase 3: one test per finding, each failing before its fix.

func intPtr(v int) *int { return &v }

// F1: a retry with the same Idempotency-Key replays after the scan token
// expired, so a lost response never turns into a second credit; a new write
// with the expired scan is still refused.
func TestRetryAfterScanExpiryReplays(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	st := e.staff("cashier")
	tok := e.scan(t, st, m)
	k := idem()
	spend := 1000
	first, err := e.svc.StaffEarn(ctx, st, k, points.StaffEarnInput{ScanToken: tok, Spend: &spend})
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(6 * time.Minute) // the 5-minute scan token is over
	again, err := e.svc.StaffEarn(ctx, st, k, points.StaffEarnInput{ScanToken: tok, Spend: &spend})
	if err != nil || again.ID != first.ID {
		t.Fatalf("retry after expiry: %+v %v, want the stored outcome %s", again, err, first.ID)
	}
	if _, err := e.svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: tok, Spend: &spend}); code(err) != "scan_invalid" {
		t.Fatalf("new write with an expired scan: %v", err)
	}
	if got := e.invariant(t, m); got != 10 {
		t.Fatalf("balance %d, want 10 (credited once)", got)
	}
}

// F2: one inconsistent account no longer stops expiry for everyone; the
// failure is still reported so the job is retried.
func TestOneBrokenAccountDoesNotStallExpiry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	past := e.clk.Now().Add(-48 * time.Hour)

	// Broken: the cached balance is below the lot it must expire.
	broken := e.member(t, e.tenant, e.app)
	e.d.Exec(t, "INSERT INTO points_accounts (member_id, tenant_id, app_id, balance) VALUES ($1, $2, $3, 0)", broken, e.tenant, e.app)
	e.d.Exec(t, "INSERT INTO points_lots (tenant_id, app_id, member_id, earned, remaining, expires_at) VALUES ($1, $2, $3, 5, 5, $4)", e.tenant, e.app, broken, past.Add(-time.Hour))

	// Healthy accounts in another tenant, expiring after the broken lot.
	otherTenant := e.d.NewTenant(t, "Beta")
	otherApp := e.newApp(t, otherTenant)
	var healthy []uuid.UUID
	for i := 0; i < 3; i++ {
		m := e.member(t, otherTenant, otherApp)
		e.d.Exec(t, "INSERT INTO points_accounts (member_id, tenant_id, app_id, balance, lifetime_earned) VALUES ($1, $2, $3, 10, 10)", m, otherTenant, otherApp)
		var lot int64
		if err := e.d.Admin.QueryRow(ctx, "INSERT INTO points_lots (tenant_id, app_id, member_id, earned, remaining, expires_at) VALUES ($1, $2, $3, 10, 10, $4) RETURNING id", otherTenant, otherApp, m, past).Scan(&lot); err != nil {
			t.Fatal(err)
		}
		e.d.Exec(t, "INSERT INTO points_ledger (tenant_id, app_id, member_id, txn_id, lot_id, delta, balance_after, reason, actor_type) VALUES ($1, $2, $3, $4, $5, 10, 10, 'earn', 'system')", otherTenant, otherApp, m, testutil.NewID(), lot)
		healthy = append(healthy, m)
	}

	n, err := e.svc.ExpireDue(ctx, e.clk.Now(), 500)
	if err == nil {
		t.Fatal("the broken lot's failure must be reported")
	}
	if n != len(healthy) {
		t.Fatalf("expired %d lots, want %d (every healthy one)", n, len(healthy))
	}
	for _, m := range healthy {
		if got := e.invariant(t, m); got != 0 {
			t.Errorf("healthy member %s still holds %d points", m, got)
		}
	}
}

// F4: fixed points from a manager respect the rule's cap, and apps without
// a rule are capped at 10,000 points per purchase.
func TestManagerFixedPointsRespectTheCap(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	mgr := e.staff("manager")

	if _, err := e.svc.StaffEarn(ctx, mgr, idem(), points.StaffEarnInput{ScanToken: e.scan(t, mgr, m), Points: intPtr(points.DefaultMaxPointsPerTxn + 1)}); code(err) != "validation_failed" {
		t.Fatalf("above the default cap: %v", err)
	}
	if _, err := e.svc.StaffEarn(ctx, mgr, idem(), points.StaffEarnInput{ScanToken: e.scan(t, mgr, m), Points: intPtr(points.DefaultMaxPointsPerTxn)}); err != nil {
		t.Fatalf("at the default cap: %v", err)
	}
	if _, err := e.svc.PutRules(ctx, e.owner, e.app, idem(), points.Rules{Currency: "TWD", SpendPerPoint: 100, PointsPerUnit: 1, MaxPointsPerTxn: intPtr(50), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StaffEarn(ctx, mgr, idem(), points.StaffEarnInput{ScanToken: e.scan(t, mgr, m), Points: intPtr(51)}); code(err) != "validation_failed" {
		t.Fatalf("above the rule's cap: %v", err)
	}
	// A huge purchase earns the cap, never more.
	spend := points.MaxSpendAmount
	tx, err := e.svc.StaffEarn(ctx, e.staff("cashier"), idem(), points.StaffEarnInput{ScanToken: e.scan(t, mgr, m), Spend: &spend})
	if code(err) != "scan_mismatch" {
		t.Fatalf("scan of another staff member: %+v %v", tx, err)
	}
	cashier := e.staff("cashier")
	tx, err = e.svc.StaffEarn(ctx, cashier, idem(), points.StaffEarnInput{ScanToken: e.scan(t, cashier, m), Spend: &spend})
	if err != nil || tx.Points != 50 {
		t.Fatalf("huge purchase: %+v %v, want 50 points", tx, err)
	}
}

// F4: staff cannot give or spend points on their own member card.
func TestStaffCannotServeTheirOwnCard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	var lineID string
	if err := e.d.Admin.QueryRow(ctx, "SELECT line_user_id FROM members WHERE id = $1", m).Scan(&lineID); err != nil {
		t.Fatal(err)
	}
	st := e.staff("manager")
	e.d.Exec(t, "INSERT INTO staff (id, tenant_id, app_id, display_name, role, line_user_id) VALUES ($1, $2, $3, 'Me', 'manager', $4)", st.StaffID, e.tenant, e.app, lineID)

	spend := 1000
	if _, err := e.svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, m), Spend: &spend}); code(err) != "self_service" {
		t.Fatalf("earn on own card: %v", err)
	}
	e.adjust(t, m, 5)
	if _, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 1, nil); code(err) != "self_service" {
		t.Fatalf("redeem on own card: %v", err)
	}
}

// F4: a scan pays for one redemption; a retry of that redemption replays.
func TestScanTokenPaysForOneRedemption(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	e.adjust(t, m, 10)
	st := e.staff("cashier")
	tok := e.scan(t, st, m)
	k := idem()
	first, err := e.svc.StaffRedeem(ctx, st, k, tok, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StaffRedeem(ctx, st, idem(), tok, 2, nil); code(err) != "scan_used" {
		t.Fatalf("second redemption on one scan: %v", err)
	}
	again, err := e.svc.StaffRedeem(ctx, st, k, tok, 2, nil)
	if err != nil || again.ID != first.ID {
		t.Fatalf("retry of the first redemption: %+v %v", again, err)
	}
	if got := e.invariant(t, m); got != 8 {
		t.Fatalf("balance %d, want 8", got)
	}
}

// F4: each staff member is limited to a number of points writes a minute.
func TestStaffWritesAreRateLimited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyring := crypto.Static(bytes.Repeat([]byte{5}, 32))
	svc := points.New(points.Deps{Pool: e.d.App, Tenants: tenant.New(e.d.App, keyring, e.clk, log), Keyring: keyring, Card: e.codec, Clock: e.clk, Log: log, StaffWritesPerMinute: 2})
	m := e.member(t, e.tenant, e.app)
	st := e.staff("cashier")
	spend := 100
	for i := 0; i < 2; i++ {
		if _, err := svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, m), Spend: &spend}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, m), Spend: &spend}); code(err) != "rate_limited" {
		t.Fatalf("third write in a minute: %v", err)
	}
	other := e.staff("cashier")
	if _, err := svc.StaffEarn(ctx, other, idem(), points.StaffEarnInput{ScanToken: e.scan(t, other, m), Spend: &spend}); err != nil {
		t.Fatalf("another staff member is not limited: %v", err)
	}
}

// F5: spend and points bounds hold in the service, not only in the spec.
func TestSpendAndPointsBoundsInTheService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	st := e.staff("cashier")
	for _, spend := range []int{-1, points.MaxSpendAmount + 1, 1 << 40} {
		s := spend
		if _, err := e.svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, m), Spend: &s}); code(err) != "validation_failed" {
			t.Errorf("staff spend %d: %v", spend, err)
		}
	}
	key := principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: e.tenant, AppID: e.app, Scopes: []string{"points:write"}}
	for _, in := range []points.ServerInput{
		{Type: points.KindEarn, Spend: intPtr(points.MaxSpendAmount + 1)},
		{Type: points.KindAdjust, Points: intPtr(points.MaxPointsPerOperation + 1)},
		{Type: points.KindAdjust, Points: intPtr(-points.MaxPointsPerOperation - 1)},
		{Type: points.KindRedeem, Points: intPtr(points.MaxPointsPerOperation + 1)},
	} {
		if _, err := e.svc.ServerPost(ctx, key, m, idem(), in); code(err) != "validation_failed" {
			t.Errorf("server %+v: %v", in, err)
		}
	}
}

// F6: points past their expiry cannot be spent, even before the job ran.
func TestExpiredPointsAreNotSpendable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, intPtr(1))
	m := e.member(t, e.tenant, e.app)
	e.earn(t, m, 1000) // 10 points, expiring in a day
	e.clk.Advance(48 * time.Hour)
	st := e.staff("cashier")
	if _, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 1, nil); code(err) != "insufficient_points" {
		t.Fatalf("redeem of expired points: %v", err)
	}
	// The next write expires them for good, in the same transaction.
	tx := e.earn(t, m, 500)
	if tx.BalanceAfter != 5 {
		t.Fatalf("balance after earn = %d, want 5 (the expired 10 are gone)", tx.BalanceAfter)
	}
	if got := e.invariant(t, m); got != 5 {
		t.Fatalf("balance %d, want 5", got)
	}
	var expired int
	if err := e.d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_ledger WHERE member_id = $1 AND reason = 'expire'", m).Scan(&expired); err != nil || expired != 1 {
		t.Fatalf("expire rows = %d (%v), want 1", expired, err)
	}
	if n, err := e.svc.ExpireDue(ctx, e.clk.Now(), 500); err != nil || n != 0 {
		t.Fatalf("job after the in-transaction expiry: %d %v", n, err)
	}
}

// F10: rotating the card also invalidates scans already made.
func TestCardRotationInvalidatesScans(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	st := e.staff("cashier")
	tok := e.scan(t, st, m) // issued for key version 1
	e.d.Exec(t, "UPDATE members SET qr_key_version = 2 WHERE id = $1", m)
	spend := 1000
	if _, err := e.svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: tok, Spend: &spend}); code(err) != "scan_invalid" {
		t.Fatalf("scan from before the rotation: %v", err)
	}
}

// F12: members do not see the brand's notes and references.
func TestMemberHistoryHidesNotesAndReferences(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	key := principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: e.tenant, AppID: e.app, Scopes: []string{"points:write"}}
	note, ref := "goodwill after complaint #4411", "R-77"
	if _, err := e.svc.ServerPost(ctx, key, m, idem(), points.ServerInput{Type: points.KindAdjust, Points: intPtr(5), RefID: &ref, Note: &note}); err != nil {
		t.Fatal(err)
	}
	self := principal.Principal{Kind: principal.KindMember, MemberID: m, AppID: e.app, TenantID: e.tenant}
	page, err := e.svc.History(ctx, self, m, 10, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("member history: %+v %v", page, err)
	}
	if page.Items[0].Note != nil || page.Items[0].RefID != nil {
		t.Errorf("member sees note %v and ref %v", page.Items[0].Note, page.Items[0].RefID)
	}
	page, err = e.svc.History(ctx, e.owner, m, 10, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Note == nil || *page.Items[0].Note != note {
		t.Fatalf("owner history must keep the note: %+v %v", page, err)
	}
}
