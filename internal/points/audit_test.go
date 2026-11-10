package points_test

import (
	"context"
	"strings"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Phase 4 review: staff points earns and redemptions are audited with the
// staff member, store, transaction and points, like coupon redemptions and
// stamps; the adjustment audit carries no note, since audit rows outlive a
// PDPA erasure.
func TestStaffPointsWritesAreAudited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, nil)
	m := e.member(t, e.tenant, e.app)
	st := e.staff("cashier")
	st.StoreID = testutil.NewID()
	spend := 500
	earned, err := e.svc.StaffEarn(ctx, st, idem(), points.StaffEarnInput{ScanToken: e.scan(t, st, m), Spend: &spend})
	if err != nil {
		t.Fatal(err)
	}
	redeemed, err := e.svc.StaffRedeem(ctx, st, idem(), e.scan(t, st, m), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		action string
		txn    points.Txn
		points string
	}{
		{points.ActionStaffEarn, earned, `"points": 5`},
		{points.ActionStaffRedeem, redeemed, `"points": -2`},
	} {
		var actorType, meta string
		if err := e.d.Admin.QueryRow(ctx, "SELECT actor_type, metadata::text FROM audit_log WHERE action = $1 AND target_id = $2",
			c.action, m.String()).Scan(&actorType, &meta); err != nil {
			t.Fatalf("%s audit row: %v", c.action, err)
		}
		for _, want := range []string{`"staff_id": "` + st.StaffID.String(), `"store_id": "` + st.StoreID.String(), `"txn_id": "` + c.txn.ID.String(), c.points} {
			if !strings.Contains(meta, want) {
				t.Errorf("%s metadata %s lacks %s", c.action, meta, want)
			}
		}
		if actorType != "staff" {
			t.Errorf("%s actor_type = %s", c.action, actorType)
		}
	}

	if _, err := e.svc.Adjust(ctx, e.owner, m, idem(), 3, "phoned 0912-345-678"); err != nil {
		t.Fatal(err)
	}
	var meta string
	if err := e.d.Admin.QueryRow(ctx, "SELECT metadata::text FROM audit_log WHERE action = 'points.adjust' AND target_id = $1", m.String()).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta, "0912") || strings.Contains(meta, "note") {
		t.Errorf("points.adjust audit metadata holds the note: %s", meta)
	}
}
