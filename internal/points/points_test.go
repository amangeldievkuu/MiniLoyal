package points_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// ---- rule arithmetic (no database)

func intp(v int) *int { return &v }

func TestPointsForRuleTable(t *testing.T) {
	def := points.DefaultRules()
	cases := []struct {
		name  string
		rules points.Rules
		spend int
		want  int64
		code  string // expected apierr code, "" for success
	}{
		{"defaults: NT$100 = 1 point", def, 250, 2, ""},
		{"defaults: exactly one unit", def, 100, 1, ""},
		{"defaults: below one unit earns nothing", def, 99, 0, "validation_failed"},
		{"zero spend", def, 0, 0, "validation_failed"},
		{"negative spend", def, -5, 0, "validation_failed"},
		{"points per unit", points.Rules{Currency: "TWD", SpendPerPoint: 50, PointsPerUnit: 3, Enabled: true}, 175, 9, ""},
		{"rounding floors partial units", points.Rules{Currency: "TWD", SpendPerPoint: 30, PointsPerUnit: 1, Enabled: true}, 89, 2, ""},
		{"min spend not met", points.Rules{Currency: "TWD", SpendPerPoint: 100, PointsPerUnit: 1, MinSpend: 300, Enabled: true}, 299, 0, "validation_failed"},
		{"min spend met", points.Rules{Currency: "TWD", SpendPerPoint: 100, PointsPerUnit: 1, MinSpend: 300, Enabled: true}, 300, 3, ""},
		{"cap", points.Rules{Currency: "TWD", SpendPerPoint: 10, PointsPerUnit: 1, MaxPointsPerTxn: intp(50), Enabled: true}, 10000, 50, ""},
		{"disabled", points.Rules{Currency: "TWD", SpendPerPoint: 100, PointsPerUnit: 1, Enabled: false}, 1000, 0, "points_disabled"},
		{"capped at the platform ceiling", points.Rules{Currency: "TWD", SpendPerPoint: 1, PointsPerUnit: 1000, Enabled: true}, 100_000_000, points.MaxEarnPerOperation, ""},
		{"spend beyond the maximum", points.Rules{Currency: "TWD", SpendPerPoint: 1, PointsPerUnit: 1, Enabled: true}, 100_000_001, 0, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.rules.PointsFor(tc.spend)
			if tc.code == "" {
				if err != nil || got != tc.want {
					t.Fatalf("PointsFor(%d) = %d, %v; want %d", tc.spend, got, err, tc.want)
				}
				return
			}
			ae, ok := apierr.As(err)
			if !ok || ae.Code != tc.code {
				t.Fatalf("PointsFor(%d) error = %v, want code %s", tc.spend, err, tc.code)
			}
		})
	}
}

// ---- fixtures

type env struct {
	d      *testutil.DB
	clk    *lockedClock
	svc    *points.Service
	codec  *card.Codec
	tenant uuid.UUID
	app    uuid.UUID
	owner  principal.Principal
	staffU principal.Principal // dashboard user with the staff role
}

// lockedClock is a fixed clock safe for concurrent readers.
type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lockedClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *lockedClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var _ clock.Clock = (*lockedClock)(nil)

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testutil.NewDB(t)
	clk := &lockedClock{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyring := crypto.Static(bytes.Repeat([]byte{5}, 32))
	codec, err := card.New(bytes.Repeat([]byte{7}, 32), clk)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{d: d, clk: clk, codec: codec}
	e.svc = points.New(points.Deps{Pool: d.App, Tenants: tenant.New(d.App, keyring, clk, log), Keyring: keyring, Card: codec, Clock: clk, Log: log, StaffWritesPerMinute: -1})
	e.tenant = d.NewTenant(t, "Alpha")
	e.app = e.newApp(t, e.tenant)
	e.owner = e.user(t, e.tenant, "owner")
	e.staffU = e.user(t, e.tenant, "staff")
	return e
}

func (e *env) newApp(t *testing.T, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	id := testutil.NewID()
	e.d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', $3)", id, tenantID, "cafe-"+id.String()[24:])
	return id
}

func (e *env) user(t *testing.T, tenantID uuid.UUID, role string) principal.Principal {
	t.Helper()
	id := testutil.NewID()
	e.d.Exec(t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", id, id.String()+"@example.com")
	e.d.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, id, role)
	return principal.Principal{Kind: principal.KindUser, UserID: id}
}

func (e *env) member(t *testing.T, tenantID, appID uuid.UUID) uuid.UUID {
	t.Helper()
	id := testutil.NewID()
	e.d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", id, tenantID, appID, "U"+id.String())
	return id
}

func (e *env) rules(t *testing.T, expiryDays *int) {
	t.Helper()
	_, err := e.svc.PutRules(context.Background(), e.owner, e.app, idem(), points.Rules{Currency: "TWD", SpendPerPoint: 100, PointsPerUnit: 1, ExpiryDays: expiryDays, Enabled: true})
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
}

func (e *env) staff(role string) principal.Principal {
	return principal.Principal{Kind: principal.KindStaff, StaffID: testutil.NewID(), StaffRole: role, AppID: e.app, TenantID: e.tenant}
}

func (e *env) scan(t *testing.T, st principal.Principal, memberID uuid.UUID) string {
	t.Helper()
	tok, _, err := e.codec.IssueScanToken(st.StaffID, memberID, st.AppID, st.TenantID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

var keyN atomic.Int64

func idem() points.Idem {
	n := keyN.Add(1)
	return points.Idem{Key: fmt.Sprintf("k-%d-%s", n, uuid.NewString()), Hash: []byte(fmt.Sprintf("h-%d", n))}
}

func (e *env) earn(t *testing.T, memberID uuid.UUID, spend int) points.Txn {
	t.Helper()
	st := e.staff("cashier")
	tx, err := e.svc.StaffEarn(context.Background(), st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, memberID), Spend: &spend})
	if err != nil {
		t.Fatalf("earn %d: %v", spend, err)
	}
	return tx
}

func (e *env) adjust(t *testing.T, memberID uuid.UUID, delta int) points.Txn {
	t.Helper()
	tx, err := e.svc.Adjust(context.Background(), e.owner, memberID, idem(), delta, "test")
	if err != nil {
		t.Fatalf("adjust %d: %v", delta, err)
	}
	return tx
}

func code(err error) string {
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	if err == nil {
		return ""
	}
	return "internal: " + err.Error()
}

// invariant checks balance = sum(ledger) = sum(open lots) for one member.
func (e *env) invariant(t *testing.T, memberID uuid.UUID) int64 {
	t.Helper()
	var balance, ledger, lots int64
	err := e.d.Admin.QueryRow(context.Background(), `
		SELECT a.balance,
		  coalesce((SELECT sum(delta) FROM points_ledger WHERE member_id = a.member_id), 0),
		  coalesce((SELECT sum(remaining) FROM points_lots WHERE member_id = a.member_id), 0)
		FROM points_accounts a WHERE a.member_id = $1`, memberID).Scan(&balance, &ledger, &lots)
	if err != nil {
		t.Fatalf("invariant: %v", err)
	}
	if balance != ledger || balance != lots || balance < 0 {
		t.Fatalf("balance %d, ledger %d, lots %d", balance, ledger, lots)
	}
	return balance
}

// ---- ledger behaviour

func TestRedeemConsumesLotsFIFOAndWritesOneRowPerLot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, intp(30))
	m := e.member(t, e.tenant, e.app)

	e.earn(t, m, 1000) // lot A: 10 points, expires day 30
	e.adjust(t, m, 5)  // lot N: 5 points, never expires
	e.clk.Advance(10 * 24 * time.Hour)
	e.earn(t, m, 700) // lot B: 7 points, expires day 40
	if got := e.invariant(t, m); got != 22 {
		t.Fatalf("balance = %d, want 22", got)
	}

	st := e.staff("cashier")
	tx, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Points != -20 || tx.BalanceAfter != 2 || tx.Kind != "redeem" {
		t.Fatalf("txn = %+v", tx)
	}
	rows, err := e.d.Admin.Query(ctx, `SELECT l.delta, t.earned FROM points_ledger l JOIN points_lots t ON t.id = l.lot_id
		WHERE l.txn_id = $1 ORDER BY l.id`, tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]int
	for rows.Next() {
		var delta, earned int
		if err := rows.Scan(&delta, &earned); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]int{delta, earned})
	}
	rows.Close()
	want := [][2]int{{-10, 10}, {-7, 7}, {-3, 5}} // soonest expiry first, never-expiring last
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ledger rows (delta, lot earned) = %v, want %v", got, want)
	}
	e.invariant(t, m)
}

func TestInsufficientPointsChangesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	e.earn(t, m, 500)
	var before int
	_ = e.d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_ledger WHERE member_id = $1", m).Scan(&before)

	st := e.staff("cashier")
	_, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 6, nil)
	if code(err) != "insufficient_points" {
		t.Fatalf("redeem 6 of 5: %v", err)
	}
	if _, err := e.svc.Adjust(ctx, e.owner, m, idem(), -6, "too much"); code(err) != "insufficient_points" {
		t.Fatalf("adjust -6 of 5: %v", err)
	}
	var after int
	_ = e.d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_ledger WHERE member_id = $1", m).Scan(&after)
	if after != before || e.invariant(t, m) != 5 {
		t.Fatalf("ledger rows %d -> %d", before, after)
	}
}

func TestStaffRulesAndScanBinding(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	cashier, manager := e.staff("cashier"), e.staff("manager")

	if _, err := e.svc.StaffEarn(ctx, cashier, idem(), points.StaffEarnInput{ScanToken: e.scan(t, cashier, m), Points: intp(50)}); code(err) != "forbidden" {
		t.Errorf("cashier fixed points: %v", err)
	}
	tx, err := e.svc.StaffEarn(ctx, manager, idem(), points.StaffEarnInput{ScanToken: e.scan(t, manager, m), Points: intp(50)})
	if err != nil || tx.Points != 50 {
		t.Fatalf("manager fixed points: %+v %v", tx, err)
	}
	if _, err := e.svc.StaffEarn(ctx, cashier, idem(), points.StaffEarnInput{ScanToken: e.scan(t, cashier, m)}); code(err) != "validation_failed" {
		t.Errorf("neither spend nor points: %v", err)
	}

	// A token for another staff member, another app, or one that expired.
	other := e.staff("cashier")
	if _, err := e.svc.StaffRedeem(ctx, cashier, idem(), e.scan(t, other, m), 1, nil); code(err) != "scan_mismatch" {
		t.Errorf("someone else's scan: %v", err)
	}
	foreignApp := e.newApp(t, e.tenant)
	tok, _, _ := e.codec.IssueScanToken(cashier.StaffID, m, foreignApp, e.tenant, 1)
	if _, err := e.svc.StaffRedeem(ctx, cashier, idem(), tok, 1, nil); code(err) != "scan_mismatch" {
		t.Errorf("scan for another app: %v", err)
	}
	old := e.scan(t, cashier, m)
	e.clk.Advance(card.ScanTokenTTL)
	if _, err := e.svc.StaffRedeem(ctx, cashier, idem(), old, 1, nil); code(err) != "scan_invalid" {
		t.Errorf("expired scan: %v", err)
	}
	if _, err := e.svc.StaffRedeem(ctx, cashier, idem(), "st1.forged.token", 1, nil); code(err) != "scan_invalid" {
		t.Errorf("forged scan: %v", err)
	}
	// A member of another app scanned by mistake: the token names the
	// member but the member is not in the staff's app.
	alien := e.member(t, e.tenant, foreignApp)
	if _, err := e.svc.StaffRedeem(ctx, cashier, idem(), e.scan(t, cashier, alien), 1, nil); code(err) != "not_found" {
		t.Errorf("member of another app: %v", err)
	}
	// The store of the staff member is recorded.
	store := testutil.NewID()
	e.d.Exec(t, "INSERT INTO stores (id, tenant_id, app_id, name) VALUES ($1, $2, $3, 'Main')", store, e.tenant, e.app)
	withStore := e.staff("cashier")
	withStore.StoreID = store
	spend := 300
	tx, err = e.svc.StaffEarn(ctx, withStore, idem(), points.StaffEarnInput{ScanToken: e.scan(t, withStore, m), Spend: &spend})
	if err != nil || tx.StoreID == nil || *tx.StoreID != store || tx.ActorType != "staff" || *tx.SpendAmount != 300 {
		t.Fatalf("store on the transaction: %+v %v", tx, err)
	}
}

func TestDisabledRulesStopEarningButNotRedeeming(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	e.earn(t, m, 1000)
	if _, err := e.svc.PutRules(ctx, e.owner, e.app, idem(), points.Rules{Currency: "TWD", SpendPerPoint: 100, PointsPerUnit: 1, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	st := e.staff("cashier")
	spend := 500
	if _, err := e.svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, m), Spend: &spend}); code(err) != "points_disabled" {
		t.Errorf("earn while disabled: %v", err)
	}
	if _, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 3, nil); err != nil {
		t.Errorf("redeem while disabled: %v", err)
	}
}

func TestIdempotentRetryReplaysAndMismatchIsRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	st := e.staff("cashier")
	tok := e.scan(t, st, m)
	spend := 400
	k := idem()
	first, err := e.svc.StaffEarn(ctx, st, k, points.StaffEarnInput{ScanToken: tok, Spend: &spend})
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.StaffEarn(ctx, st, k, points.StaffEarnInput{ScanToken: tok, Spend: &spend})
	if err != nil || again.ID != first.ID || e.invariant(t, m) != 4 {
		t.Fatalf("replay: %+v %v", again, err)
	}
	k.Hash = []byte("different body")
	if _, err := e.svc.StaffEarn(ctx, st, k, points.StaffEarnInput{ScanToken: tok, Spend: &spend}); code(err) != "idempotency_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
}

// ---- concurrency

func TestConcurrentRedeemsNeverOverspend(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	for i := 0; i < 6; i++ {
		e.earn(t, m, 10000) // six lots of 100
	}
	const attempts, workers = 1000, 50
	var ok, insufficient atomic.Int64
	var other sync.Map
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := e.staff("cashier")
			for range jobs {
				_, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 1, nil)
				switch code(err) {
				case "":
					ok.Add(1)
				case "insufficient_points":
					insufficient.Add(1)
				default:
					other.Store(code(err), true)
				}
			}
		}()
	}
	for i := 0; i < attempts; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	other.Range(func(k, _ any) bool { t.Errorf("unexpected error: %v", k); return true })
	if ok.Load() != 600 || insufficient.Load() != 400 {
		t.Fatalf("succeeded %d, insufficient %d; want 600 and 400", ok.Load(), insufficient.Load())
	}
	if got := e.invariant(t, m); got != 0 {
		t.Fatalf("balance = %d, want 0", got)
	}
	var negative int
	_ = e.d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_ledger WHERE member_id = $1 AND balance_after < 0", m).Scan(&negative)
	if negative != 0 {
		t.Fatalf("%d ledger rows recorded a negative balance", negative)
	}
}

func TestConcurrentEarnsAndRedeemsKeepTheInvariant(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	e.earn(t, m, 2000)
	var wg sync.WaitGroup
	for w := 0; w < 20; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			st := e.staff("cashier")
			for i := 0; i < 15; i++ {
				var err error
				if (w+i)%2 == 0 {
					spend := 100 * (1 + i%3)
					_, err = e.svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, m), Spend: &spend})
				} else {
					_, err = e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 1+i%4, nil)
				}
				if c := code(err); c != "" && c != "insufficient_points" {
					t.Errorf("worker %d: %v", w, err)
				}
			}
		}(w)
	}
	wg.Wait()
	e.invariant(t, m)
}

// ---- expiry and reconciliation

func TestExpiryIsExactIdempotentAndRaceSafe(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, intp(30))
	m1, m2 := e.member(t, e.tenant, e.app), e.member(t, e.tenant, e.app)
	e.earn(t, m1, 1000) // 10, expires day 30
	e.adjust(t, m1, 4)  // never expires
	e.clk.Advance(5 * 24 * time.Hour)
	e.earn(t, m1, 300) // 3, expires day 35
	e.earn(t, m2, 800) // 8, expires day 35
	// m1 spends 2 of the oldest lot first.
	st := e.staff("cashier")
	if _, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m1), 2, nil); err != nil {
		t.Fatal(err)
	}

	day31 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC).Add(31 * 24 * time.Hour)
	n, err := e.svc.ExpireDue(ctx, day31, 100)
	if err != nil || n != 1 {
		t.Fatalf("day 31 expired %d lots, %v; want 1", n, err)
	}
	if e.invariant(t, m1) != 7 || e.invariant(t, m2) != 8 {
		t.Fatal("day 31 balances wrong")
	}
	if n, err := e.svc.ExpireDue(ctx, day31, 100); err != nil || n != 0 {
		t.Fatalf("second run expired %d, %v", n, err)
	}

	// Day 36: two lots are due; two runs race.
	day36 := day31.Add(5 * 24 * time.Hour)
	var wg sync.WaitGroup
	var total atomic.Int64
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := e.svc.ExpireDue(ctx, day36, 100)
			if err != nil {
				t.Errorf("racing run: %v", err)
			}
			total.Add(int64(n))
		}()
	}
	wg.Wait()
	if total.Load() != 2 {
		t.Fatalf("racing runs expired %d lots, want 2", total.Load())
	}
	if e.invariant(t, m1) != 4 || e.invariant(t, m2) != 0 {
		t.Fatal("day 36 balances wrong: only the never-expiring lot should remain")
	}
	var expireRows int
	_ = e.d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_ledger WHERE reason = 'expire'").Scan(&expireRows)
	if expireRows != 3 {
		t.Fatalf("expire rows = %d, want 3 (one per lot)", expireRows)
	}
	// History shows the expiry as its own transaction.
	page, err := e.svc.History(ctx, e.owner, m2, 10, "")
	if err != nil || len(page.Items) != 2 || page.Items[0].Kind != "expire" || page.Items[0].Points != -8 {
		t.Fatalf("history after expiry: %+v %v", page, err)
	}
}

func TestReconcileReportsTamperedBalances(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m1, m2 := e.member(t, e.tenant, e.app), e.member(t, e.tenant, e.app)
	e.earn(t, m1, 500)
	e.earn(t, m2, 900)
	checked, mismatches, err := e.svc.Reconcile(ctx, e.clk.Now())
	if err != nil || checked != 2 || mismatches != 0 {
		t.Fatalf("clean run: %d checked, %d mismatches, %v", checked, mismatches, err)
	}
	e.d.Exec(t, "UPDATE points_accounts SET balance = balance + 1000 WHERE member_id = $1", m2)
	checked, mismatches, err = e.svc.Reconcile(ctx, e.clk.Now())
	if err != nil || checked != 2 || mismatches != 1 {
		t.Fatalf("tampered run: %d checked, %d mismatches, %v", checked, mismatches, err)
	}
	var runs, recorded int
	var details string
	_ = e.d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_reconciliation_runs WHERE finished_at IS NOT NULL").Scan(&runs)
	_ = e.d.Admin.QueryRow(ctx, "SELECT mismatches, details::text FROM points_reconciliation_runs ORDER BY id DESC LIMIT 1").Scan(&recorded, &details)
	if runs != 2 || recorded != 1 || !bytes.Contains([]byte(details), []byte(m2.String())) {
		t.Fatalf("runs %d, last mismatches %d, details %s", runs, recorded, details)
	}
}

// ---- reads, rules and access

func TestSummaryHistoryAndPaging(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, intp(10))
	m := e.member(t, e.tenant, e.app)
	for i := 1; i <= 5; i++ {
		e.earn(t, m, 100*i)
		e.clk.Advance(time.Hour)
	}
	self := principal.Principal{Kind: principal.KindMember, MemberID: m, AppID: e.app, TenantID: e.tenant}
	sum, err := e.svc.Summary(ctx, self, m)
	if err != nil || sum.Balance != 15 || sum.LifetimeEarned != 15 || len(sum.Expiring) != 3 || sum.Expiring[0].Points != 1 || sum.Rules.ExpiryDays == nil {
		t.Fatalf("summary: %+v %v", sum, err)
	}
	var seen []int64
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := e.svc.History(ctx, self, m, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			seen = append(seen, it.Points)
		}
		if page.NextCursor == "" {
			if pages != 2 {
				t.Errorf("pages = %d, want 3", pages+1)
			}
			break
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(seen) != "[5 4 3 2 1]" {
		t.Fatalf("history newest first = %v", seen)
	}
	for _, bad := range []string{"!!", "bm90LWEtbnVtYmVy", "LTU"} {
		if _, err := e.svc.History(ctx, self, m, 2, bad); code(err) != "bad_request" {
			t.Errorf("cursor %q: %v", bad, err)
		}
	}
	// A member reads only themselves.
	other := e.member(t, e.tenant, e.app)
	if _, err := e.svc.Summary(ctx, self, other); code(err) != "not_found" {
		t.Errorf("member reading another member: %v", err)
	}
	// A member without any points has a zero summary.
	sum, err = e.svc.Summary(ctx, e.owner, other)
	if err != nil || sum.Balance != 0 || len(sum.Expiring) != 0 {
		t.Fatalf("empty summary: %+v %v", sum, err)
	}
}

func TestRolesAndCrossTenantAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	e.earn(t, m, 1000)

	if _, err := e.svc.Summary(ctx, e.staffU, m); err != nil {
		t.Errorf("staff role reading a summary: %v", err)
	}
	if _, err := e.svc.Adjust(ctx, e.staffU, m, idem(), 5, "x"); code(err) != "forbidden" {
		t.Errorf("staff role adjusting: %v", err)
	}
	if _, err := e.svc.PutRules(ctx, e.staffU, e.app, idem(), points.DefaultRules()); code(err) != "forbidden" {
		t.Errorf("staff role changing rules: %v", err)
	}
	otherTenant := e.d.NewTenant(t, "Beta")
	outsider := e.user(t, otherTenant, "owner")
	for name, err := range map[string]error{
		"summary": func() error { _, err := e.svc.Summary(ctx, outsider, m); return err }(),
		"history": func() error { _, err := e.svc.History(ctx, outsider, m, 10, ""); return err }(),
		"adjust":  func() error { _, err := e.svc.Adjust(ctx, outsider, m, idem(), 5, "x"); return err }(),
		"rules":   func() error { _, err := e.svc.GetRules(ctx, outsider, e.app); return err }(),
		"put": func() error {
			_, err := e.svc.PutRules(ctx, outsider, e.app, idem(), points.DefaultRules())
			return err
		}(),
	} {
		if code(err) != "not_found" {
			t.Errorf("outsider %s: %v", name, err)
		}
	}
	// Rule validation.
	if _, err := e.svc.PutRules(ctx, e.owner, e.app, idem(), points.Rules{Currency: "taiwan", SpendPerPoint: 0, PointsPerUnit: 0, MinSpend: -1, ExpiryDays: intp(0), Enabled: true}); code(err) != "validation_failed" {
		t.Errorf("invalid rules: %v", err)
	}
	// Erased members are gone for points too.
	e.d.Exec(t, "UPDATE members SET anonymized_at = now() WHERE id = $1", m)
	if _, err := e.svc.Summary(ctx, e.owner, m); code(err) != "not_found" {
		t.Errorf("erased member: %v", err)
	}
}

func TestServerAPIStaysInsideItsApp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	key := principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: e.tenant, AppID: e.app, Scopes: []string{"points:write"}}

	tx, err := e.svc.ServerPost(ctx, key, m, idem(), points.ServerInput{Type: "earn", Spend: intp(1250), RefType: strp("receipt"), RefID: strp("R-1")})
	if err != nil || tx.Points != 12 || *tx.RefID != "R-1" || tx.ActorType != "api_key" {
		t.Fatalf("server earn: %+v %v", tx, err)
	}
	if _, err := e.svc.ServerPost(ctx, key, m, idem(), points.ServerInput{Type: "redeem", Points: intp(2)}); err != nil {
		t.Fatalf("server redeem: %v", err)
	}
	if tx, err := e.svc.ServerPost(ctx, key, m, idem(), points.ServerInput{Type: "adjust", Points: intp(-3)}); err != nil || tx.BalanceAfter != 7 {
		t.Fatalf("server adjust: %+v %v", tx, err)
	}
	for name, in := range map[string]points.ServerInput{
		"unknown type":  {Type: "gift", Points: intp(1)},
		"earn both":     {Type: "earn", Points: intp(1), Spend: intp(100)},
		"redeem zero":   {Type: "redeem", Points: intp(0)},
		"adjust zero":   {Type: "adjust", Points: intp(0)},
		"redeem spend":  {Type: "redeem", Points: intp(1), Spend: intp(1)},
		"ref too long":  {Type: "earn", Points: intp(1), RefID: strp(string(bytes.Repeat([]byte("x"), 129)))},
		"note with NUL": {Type: "earn", Points: intp(1), Note: strp("a\x00b")},
	} {
		if _, err := e.svc.ServerPost(ctx, key, m, idem(), in); code(err) != "validation_failed" {
			t.Errorf("%s: %v", name, err)
		}
	}
	otherApp := e.newApp(t, e.tenant)
	foreign := e.member(t, e.tenant, otherApp)
	if _, err := e.svc.ServerPost(ctx, key, foreign, idem(), points.ServerInput{Type: "earn", Points: intp(1)}); code(err) != "not_found" {
		t.Errorf("member of another app: %v", err)
	}
	if _, err := e.svc.Summary(ctx, key, foreign); code(err) != "not_found" {
		t.Errorf("summary of another app's member: %v", err)
	}
	if _, err := e.svc.ServerPost(ctx, key, testutil.NewID(), idem(), points.ServerInput{Type: "earn", Points: intp(1)}); code(err) != "not_found" {
		t.Errorf("unknown member: %v", err)
	}
}

func strp(s string) *string { return &s }
