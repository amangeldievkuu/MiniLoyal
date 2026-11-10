package points_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Invariant: every points write records one points.changed event per
// transaction in the same database transaction, carrying IDs and amounts
// only: earns, redemptions, adjustments, the expiry a write performs first,
// and the expiry job.
func TestEveryPointsWriteEmitsPointsChanged(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, intp(30))
	m := e.member(t, e.tenant, e.app)
	other := e.member(t, e.tenant, e.app)

	earned := e.earn(t, m, 500) // 5 points, expiring in 30 days
	adjusted := e.adjust(t, m, 10)
	st := e.staff("cashier")
	redeemed, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.earn(t, other, 200)

	// Past the expiry: the next write expires the lot first, and the job
	// expires the other member's lot.
	e.clk.Advance(31 * 24 * time.Hour)
	e.adjust(t, m, 1)
	if _, err := e.svc.ExpireDue(ctx, e.clk.Now(), 100); err != nil {
		t.Fatal(err)
	}

	type row struct {
		member  uuid.UUID
		kind    string
		points  int64
		balance int64
		txn     string
	}
	rows, err := e.d.Admin.Query(ctx, `SELECT member_id, payload->>'kind', (payload->>'points')::bigint,
		(payload->>'balance_after')::bigint, payload->>'txn_id'
		FROM outbound_events WHERE type = 'points.changed' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.member, &r.kind, &r.points, &r.balance, &r.txn); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []row{
		{m, "earn", 5, 5, earned.ID.String()},
		{m, "adjust", 10, 15, adjusted.ID.String()},
		{m, "redeem", -3, 12, redeemed.ID.String()},
		{other, "earn", 2, 2, ""},
		// FIFO took the 3 points from the earned lot, so 2 expire.
		{m, "expire", -2, 10, ""},
		{m, "adjust", 1, 11, ""},
		{other, "expire", -2, 0, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.member != w.member || g.kind != w.kind || g.points != w.points || g.balance != w.balance || (w.txn != "" && g.txn != w.txn) {
			t.Errorf("event %d = %+v, want %+v", i, g, w)
		}
	}
	// Each event belongs to a ledger transaction of its member.
	var orphans int
	if err := e.d.Admin.QueryRow(ctx, `SELECT count(*) FROM outbound_events o WHERE type = 'points.changed'
		AND NOT EXISTS (SELECT 1 FROM points_ledger l WHERE l.txn_id = (o.payload->>'txn_id')::uuid AND l.member_id = o.member_id)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d points.changed events name no ledger transaction", orphans)
	}
	// A refused write emits nothing.
	before := countEvents(t, e)
	if _, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 1000, nil); code(err) != "insufficient_points" {
		t.Fatalf("overdraw: %v", err)
	}
	if after := countEvents(t, e); after != before {
		t.Errorf("a refused redeem added %d events", after-before)
	}
}

func countEvents(t *testing.T, e *env) int {
	t.Helper()
	var n int
	if err := e.d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM outbound_events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
