package points_test

import (
	"context"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/points"
)

// modelLot mirrors one points_lots row.
type modelLot struct {
	remaining int64
	expires   *time.Time
}

// model is the expected state of one member: lots in creation order, which
// is also id order, so model lot i is the member's i-th lot row.
type model struct {
	id   uuid.UUID
	lots []*modelLot
}

func (m *model) balance() int64 {
	var b int64
	for _, l := range m.lots {
		b += l.remaining
	}
	return b
}

// consume takes n points FIFO the way the service does: soonest expiry
// first, never-expiring last, then oldest. A lot past its expiry that the
// job has not processed yet is still open.
func (m *model) consume(n int64) {
	order := slices.Clone(m.lots)
	slices.SortStableFunc(order, func(a, b *modelLot) int {
		switch {
		case a.expires == nil && b.expires == nil:
			return 0
		case a.expires == nil:
			return 1
		case b.expires == nil:
			return -1
		}
		return a.expires.Compare(*b.expires)
	})
	for _, l := range order {
		take := min(l.remaining, n)
		l.remaining -= take
		n -= take
	}
}

// snapshot records what every lot holds, for restore after a refused write.
func (m *model) snapshot() []int64 {
	out := make([]int64, len(m.lots))
	for i, l := range m.lots {
		out[i] = l.remaining
	}
	return out
}

func (m *model) restore(s []int64) {
	for i, r := range s {
		m.lots[i].remaining = r
	}
}

// expire empties every lot due at now and returns how many it emptied.
func (m *model) expire(now time.Time) int {
	n := 0
	for _, l := range m.lots {
		if l.expires != nil && !l.expires.After(now) && l.remaining > 0 {
			l.remaining = 0
			n++
		}
	}
	return n
}

// checkLedger asserts, for one member, that the cached balance equals the
// ledger sum and the open lots, that the newest ledger row carries the
// balance, that every lot is within 0..earned, and that the lots match the
// model one for one.
func (e *env) checkLedger(t *testing.T, step int, m *model) {
	t.Helper()
	ctx := context.Background()
	var balance, ledger, lots, lastAfter int64
	var rows int
	err := e.d.Admin.QueryRow(ctx, `
		SELECT coalesce((SELECT balance FROM points_accounts WHERE member_id = $1), 0),
		  coalesce((SELECT sum(delta) FROM points_ledger WHERE member_id = $1), 0),
		  coalesce((SELECT sum(remaining) FROM points_lots WHERE member_id = $1), 0),
		  coalesce((SELECT balance_after FROM points_ledger WHERE member_id = $1 ORDER BY id DESC LIMIT 1), 0),
		  (SELECT count(*) FROM points_ledger WHERE member_id = $1)`, m.id).Scan(&balance, &ledger, &lots, &lastAfter, &rows)
	if err != nil {
		t.Fatalf("step %d: %v", step, err)
	}
	if balance != ledger || balance != lots {
		t.Fatalf("step %d member %s: balance %d, ledger %d, lots %d", step, m.id, balance, ledger, lots)
	}
	if rows > 0 && lastAfter != balance {
		t.Fatalf("step %d member %s: last balance_after %d, balance %d", step, m.id, lastAfter, balance)
	}
	if want := m.balance(); balance != want {
		t.Fatalf("step %d member %s: balance %d, model %d", step, m.id, balance, want)
	}
	r, err := e.d.Admin.Query(ctx, "SELECT earned, remaining FROM points_lots WHERE member_id = $1 ORDER BY id", m.id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	i := 0
	for r.Next() {
		var earned, remaining int64
		if err := r.Scan(&earned, &remaining); err != nil {
			t.Fatal(err)
		}
		if remaining < 0 || remaining > earned {
			t.Fatalf("step %d member %s: lot %d has %d of %d", step, m.id, i, remaining, earned)
		}
		if i >= len(m.lots) || m.lots[i].remaining != remaining {
			t.Fatalf("step %d member %s: lot %d remaining %d, model %+v", step, m.id, i, remaining, m.lots)
		}
		i++
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if i != len(m.lots) {
		t.Fatalf("step %d member %s: %d lots, model %d", step, m.id, i, len(m.lots))
	}
}

// Invariant: under any sequence of earn, redeem, adjust and expiry, every
// member's balance equals the ledger sum and the open lots, the newest
// ledger row carries the balance, lots stay within 0..earned, the outcome
// matches a FIFO model (including which operations are refused), and
// Reconcile reports no mismatch. The sequence is seeded, so a failure
// replays exactly.
func TestLedgerPropertyRandomSequences(t *testing.T) {
	const seed, steps, members = 20261110, 200, 4
	e := newEnv(t)
	ctx := context.Background()
	expiry := 10
	e.rules(t, intp(expiry))
	rng := rand.New(rand.NewPCG(seed, seed))
	t.Logf("seed %d", seed)

	ms := make([]*model, members)
	for i := range ms {
		ms[i] = &model{id: e.member(t, e.tenant, e.app)}
	}
	counts := map[string]int{}
	for step := 0; step < steps; step++ {
		m := ms[rng.IntN(members)]
		// Time moves on between operations, so lots get distinct expiries
		// and some fall due between expiry runs.
		e.clk.Advance(time.Duration(rng.IntN(36)) * time.Hour)
		now := e.clk.Now()
		p := rng.IntN(100)
		// Every write expires the member's due lots first, in its own
		// transaction (security review Phase 3, F6); a refused write rolls
		// that back, so the model restores its lots on a refusal.
		before := m.snapshot()
		if p < 85 {
			m.expire(now)
		}
		bal := m.balance()

		switch {
		case p < 35: // earn through the till
			spend := 100 + rng.IntN(4901)
			pts := int64(spend / 100)
			tx := e.earn(t, m.id, spend)
			if tx.Points != pts {
				t.Fatalf("step %d: earned %d, want %d", step, tx.Points, pts)
			}
			exp := now.Add(time.Duration(expiry) * 24 * time.Hour)
			m.lots = append(m.lots, &modelLot{remaining: pts, expires: &exp})
			counts["earn"]++
		case p < 65: // redeem, sometimes more than the balance
			n := 1 + rng.Int64N(bal+5)
			st := e.staff("cashier")
			tx, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m.id), int(n), nil)
			if n > bal {
				if code(err) != "insufficient_points" {
					t.Fatalf("step %d: redeem %d of %d: %v", step, n, bal, err)
				}
				m.restore(before)
				counts["redeem refused"]++
				break
			}
			if err != nil || tx.BalanceAfter != bal-n {
				t.Fatalf("step %d: redeem %d of %d: %+v %v", step, n, bal, tx, err)
			}
			m.consume(n)
			counts["redeem"]++
		case p < 85: // adjust either way, sometimes below zero
			delta := rng.Int64N(bal+54) - (bal + 3)
			if delta == 0 {
				delta = 1
			}
			tx, err := e.svc.Adjust(ctx, e.owner, m.id, idem(), int(delta), "property")
			if bal+delta < 0 {
				if code(err) != "insufficient_points" {
					t.Fatalf("step %d: adjust %d of %d: %v", step, delta, bal, err)
				}
				m.restore(before)
				counts["adjust refused"]++
				break
			}
			if err != nil || tx.BalanceAfter != bal+delta {
				t.Fatalf("step %d: adjust %d of %d: %+v %v", step, delta, bal, tx, err)
			}
			if delta > 0 {
				m.lots = append(m.lots, &modelLot{remaining: delta})
			} else {
				m.consume(-delta)
			}
			counts["adjust"]++
		default: // the expiry job, possibly days later
			e.clk.Advance(time.Duration(rng.IntN(6*24)) * time.Hour)
			now = e.clk.Now()
			want := 0
			for _, mm := range ms {
				want += mm.expire(now)
			}
			n, err := e.svc.ExpireDue(ctx, now, 500)
			if err != nil || n != want {
				t.Fatalf("step %d: expired %d lots, %v; model %d", step, n, err, want)
			}
			counts["expire"]++
		}

		for _, mm := range ms {
			e.checkLedger(t, step, mm)
		}
		if _, mismatches, err := e.svc.Reconcile(ctx, now); err != nil || mismatches != 0 {
			t.Fatalf("step %d: reconcile %d mismatches, %v", step, mismatches, err)
		}
	}
	t.Logf("operations: %v", counts)
	for _, kind := range []string{"earn", "redeem", "redeem refused", "adjust", "adjust refused", "expire"} {
		if counts[kind] == 0 {
			t.Errorf("the sequence never exercised %q; change the seed", kind)
		}
	}
}

// Invariant: expiry runs racing redemptions on the same member never
// overspend or double count. Every due lot is expired at most once, the
// points removed by redemptions and expiry add up to what was there, and
// the ledger, lots and balance agree afterwards.
func TestExpiryRacingRedeemsKeepsTheLedger(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, intp(10))
	m := e.member(t, e.tenant, e.app)
	start := e.clk.Now()
	for i := 0; i < 10; i++ {
		e.earn(t, m, 1000) // 10 points each, due on day 10
	}
	e.clk.Advance(5 * 24 * time.Hour)
	for i := 0; i < 10; i++ {
		e.earn(t, m, 1000) // 10 points each, due on day 15
	}
	e.adjust(t, m, 50) // never expires
	const total = 250
	e.clk.Advance(7 * 24 * time.Hour) // day 12: the first ten lots are due
	now := e.clk.Now()

	const redeemers, perRedeemer, expirers, runs = 20, 10, 4, 5
	var redeemed, refused atomic.Int64
	var wg sync.WaitGroup
	release := make(chan struct{})
	for r := 0; r < redeemers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := e.staff("cashier")
			<-release
			for i := 0; i < perRedeemer; i++ {
				_, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 1, nil)
				switch code(err) {
				case "":
					redeemed.Add(1)
				case "insufficient_points":
					refused.Add(1)
				default:
					t.Errorf("redeem: %v", err)
				}
			}
		}()
	}
	for x := 0; x < expirers; x++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			for i := 0; i < runs; i++ {
				if _, err := e.svc.ExpireDue(ctx, now, 3); err != nil {
					t.Errorf("expire: %v", err)
				}
			}
		}()
	}
	close(release)
	wg.Wait()
	// Whatever the race left behind, one more run expires it.
	if _, err := e.svc.ExpireDue(ctx, now, 500); err != nil {
		t.Fatal(err)
	}

	balance := e.invariant(t, m)
	var expired, redeemedLedger, dueLeft, doubleExpired int64
	if err := e.d.Admin.QueryRow(ctx, `
		SELECT coalesce(-sum(delta) FILTER (WHERE reason = 'expire'), 0),
		       coalesce(-sum(delta) FILTER (WHERE reason = 'redeem'), 0)
		FROM points_ledger WHERE member_id = $1`, m).Scan(&expired, &redeemedLedger); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Admin.QueryRow(ctx, `SELECT coalesce(sum(remaining), 0) FROM points_lots WHERE member_id = $1 AND expires_at <= $2`, m, now).Scan(&dueLeft); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Admin.QueryRow(ctx, `SELECT count(*) FROM (SELECT lot_id FROM points_ledger WHERE member_id = $1 AND reason = 'expire' GROUP BY lot_id HAVING count(*) > 1) d`, m).Scan(&doubleExpired); err != nil {
		t.Fatal(err)
	}
	if redeemedLedger != redeemed.Load() || redeemed.Load()+refused.Load() != redeemers*perRedeemer {
		t.Fatalf("ledger redeemed %d, callers saw %d succeed and %d refused", redeemedLedger, redeemed.Load(), refused.Load())
	}
	if balance+expired+redeemedLedger != total {
		t.Fatalf("balance %d + expired %d + redeemed %d != %d", balance, expired, redeemedLedger, total)
	}
	if dueLeft != 0 || doubleExpired != 0 {
		t.Fatalf("due lots still hold %d points; %d lots expired twice", dueLeft, doubleExpired)
	}
	// The due lots held 100 points: each went either to a redemption or to expiry.
	var fromDue int64
	if err := e.d.Admin.QueryRow(ctx, `
		SELECT coalesce(-sum(l.delta), 0) FROM points_ledger l JOIN points_lots t ON t.id = l.lot_id
		WHERE t.member_id = $1 AND t.expires_at = $2 AND l.delta < 0`, m, start.Add(10*24*time.Hour)).Scan(&fromDue); err != nil {
		t.Fatal(err)
	}
	if fromDue != 100 {
		t.Fatalf("the due lots gave up %d points, want 100", fromDue)
	}
	if _, mismatches, err := e.svc.Reconcile(ctx, now); err != nil || mismatches != 0 {
		t.Fatalf("reconcile: %d mismatches, %v", mismatches, err)
	}
}

// Invariant: points past their expiry cannot be spent. The expiry job runs
// every 15 minutes, so between a lot's expires_at and the next run the lot
// is still open; a redemption in that gap must not consume it.
func TestExpiredPointsCannotBeRedeemedBeforeTheJobRuns(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, intp(10))
	m := e.member(t, e.tenant, e.app)
	e.earn(t, m, 1000) // 10 points, due on day 10
	e.clk.Advance(10*24*time.Hour + time.Minute)

	st := e.staff("cashier")
	tx, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 10, nil)
	if err == nil {
		t.Fatalf("regression of security review Phase 3, F6: a redemption %s after the lot's expiry spent %d expired points (balance after %d); "+
			"internal/points/service.go apply consumes ListOpenLotsForUpdate, which does not exclude lots with expires_at <= now, "+
			"so expired points stay spendable until the next ExpireDue run (up to jobs.ExpiryInterval); "+
			"the summary's balance also still counts them", time.Minute, -tx.Points, tx.BalanceAfter)
	}
	if code(err) != "insufficient_points" {
		t.Fatalf("redeem expired points: %v", err)
	}
	if got, err := e.svc.Summary(ctx, e.owner, m); err != nil || got.Balance != 0 {
		t.Fatalf("summary after expiry: %+v %v", got, err)
	}
}

// Invariant: the operation bounds hold at the spec maxima. The largest
// spend under the most generous rule is refused, not overflowed; the cap
// applies before the bound; points at the maximum succeed and one more is
// refused; an adjustment to exactly zero is allowed and leaves no lot open.
func TestOperationBoundsAtTheSpecMaxima(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	manager := e.staff("manager")
	put := func(r points.Rules) {
		t.Helper()
		r.Currency, r.Enabled = "TWD", true
		if _, err := e.svc.PutRules(ctx, e.owner, e.app, idem(), r); err != nil {
			t.Fatalf("rules %+v: %v", r, err)
		}
	}
	earn := func(spend, pts *int) (points.Txn, error) {
		return e.svc.StaffEarn(ctx, manager, idem(), points.StaffEarnInput{ScanToken: e.scan(t, manager, m), Spend: spend, Points: pts})
	}

	// spend_amount 100,000,000 (the spec maximum) at 1 currency unit = 1000
	// points would be 10^11 points: the rule math does not overflow and one
	// purchase earns the platform ceiling (security review Phase 3, F4/F5).
	put(points.Rules{SpendPerPoint: 1, PointsPerUnit: 1000})
	tx, err := earn(intp(100_000_000), nil)
	if err != nil || tx.Points != points.MaxEarnPerOperation || tx.SpendAmount == nil || *tx.SpendAmount != 100_000_000 {
		t.Fatalf("max spend, generous rule: %+v %v", tx, err)
	}
	// A rule cap above the ceiling still earns the ceiling.
	put(points.Rules{SpendPerPoint: 1, PointsPerUnit: 1000, MaxPointsPerTxn: intp(points.MaxPointsPerOperation)})
	if tx, err := earn(intp(100_000_000), nil); err != nil || tx.Points != points.MaxEarnPerOperation {
		t.Fatalf("max spend, capped above the ceiling: %+v %v", tx, err)
	}
	put(points.Rules{SpendPerPoint: 100, PointsPerUnit: 1, MaxPointsPerTxn: intp(5)})
	if tx, err := earn(intp(1_000_000), nil); err != nil || tx.Points != 5 {
		t.Fatalf("cap of 5: %+v %v", tx, err)
	}
	// Fixed points respect the rule cap, then the platform ceiling.
	if tx, err := earn(nil, intp(5)); err != nil || tx.Points != 5 {
		t.Fatalf("fixed points at the cap: %+v %v", tx, err)
	}
	if _, err := earn(nil, intp(6)); code(err) != "validation_failed" {
		t.Fatalf("fixed points above the cap: %v", err)
	}
	put(points.Rules{SpendPerPoint: 100, PointsPerUnit: 1})
	if tx, err := earn(nil, intp(points.MaxEarnPerOperation)); err != nil || tx.Points != points.MaxEarnPerOperation {
		t.Fatalf("max fixed points: %+v %v", tx, err)
	}
	if _, err := earn(nil, intp(points.MaxEarnPerOperation+1)); code(err) != "validation_failed" {
		t.Fatalf("max+1 fixed points: %v", err)
	}
	for _, bad := range []int{0, -1} {
		if _, err := earn(nil, intp(bad)); code(err) != "validation_failed" {
			t.Errorf("fixed points %d: %v", bad, err)
		}
		if _, err := e.svc.StaffRedeem(ctx, manager, idem(), e.scan(t, manager, m), bad, nil); code(err) != "validation_failed" {
			t.Errorf("redeem %d: %v", bad, err)
		}
	}
	if _, err := e.svc.StaffRedeem(ctx, manager, idem(), e.scan(t, manager, m), points.MaxPointsPerOperation+1, nil); code(err) != "validation_failed" {
		t.Errorf("redeem max+1: %v", err)
	}
	if _, err := e.svc.Adjust(ctx, e.owner, m, idem(), points.MaxPointsPerOperation+1, "too big"); code(err) != "validation_failed" {
		t.Errorf("adjust max+1: %v", err)
	}
	if _, err := e.svc.Adjust(ctx, e.owner, m, idem(), -points.MaxPointsPerOperation-1, "too small"); code(err) != "validation_failed" {
		t.Errorf("adjust -(max+1): %v", err)
	}

	// Adjust down to exactly zero, in steps of at most the maximum.
	for bal := e.invariant(t, m); bal > 0; bal = e.invariant(t, m) {
		step := min(bal, points.MaxPointsPerOperation)
		if tx, err := e.svc.Adjust(ctx, e.owner, m, idem(), -int(step), "close account"); err != nil || tx.BalanceAfter != bal-step {
			t.Fatalf("adjust -%d of %d: %+v %v", step, bal, tx, err)
		}
	}
	var open int
	_ = e.d.Admin.QueryRow(ctx, "SELECT count(*) FROM points_lots WHERE member_id = $1 AND remaining > 0", m).Scan(&open)
	if open != 0 {
		t.Fatalf("%d lots still open at a zero balance", open)
	}
	if _, err := e.svc.Adjust(ctx, e.owner, m, idem(), -1, "below zero"); code(err) != "insufficient_points" {
		t.Fatalf("adjust below zero: %v", err)
	}
	var lifetime int64
	_ = e.d.Admin.QueryRow(ctx, "SELECT lifetime_earned FROM points_accounts WHERE member_id = $1", m).Scan(&lifetime)
	if lifetime != 3*points.MaxEarnPerOperation+10 {
		t.Errorf("lifetime earned = %d", lifetime)
	}
}
