package points_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Invariant (test-engineer rule 4): 200 goroutines released at once against
// one account, half staff redeems and half dashboard deductions, spend
// exactly the balance and no more. The balance never goes negative and the
// cached balance equals the ledger sum and the open lots.
func TestTwoHundredSimultaneousDebitsNeverOverspend(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	m := e.member(t, e.tenant, e.app)
	e.adjust(t, m, 500)
	const n, each = 200, 5

	staff := e.staff("cashier")
	tokens := make([]string, n)
	for i := range tokens {
		tokens[i] = e.scan(t, staff, m)
	}
	codes := make([]string, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			var err error
			if i%2 == 0 {
				_, err = e.svc.StaffRedeem(context.Background(), staff, idem(), tokens[i], each, nil)
			} else {
				_, err = e.svc.Adjust(context.Background(), e.owner, m, idem(), -each, "deduction")
			}
			codes[i] = code(err)
		}()
	}
	close(release)
	wg.Wait()

	ok := 0
	for i, c := range codes {
		switch c {
		case "":
			ok++
		case "insufficient_points":
		default:
			t.Fatalf("debit %d: %s", i, c)
		}
	}
	if ok != 100 {
		t.Errorf("%d debits of %d succeeded, want 100 (the whole balance)", ok, each)
	}
	if got := e.invariant(t, m); got != 0 {
		t.Errorf("balance = %d, want 0", got)
	}
	var negative int
	if err := e.d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM points_ledger WHERE member_id = $1 AND balance_after < 0", m).Scan(&negative); err != nil {
		t.Fatal(err)
	}
	if negative != 0 {
		t.Errorf("%d ledger rows recorded a negative balance", negative)
	}
	var events int
	if err := e.d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM outbound_events WHERE type = 'points.changed' AND member_id = $1", m).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != ok+1 {
		t.Errorf("points.changed events = %d, want %d (one per successful write)", events, ok+1)
	}
}

// Invariant: any team member reads the points rules of their own tenant's
// app; other credential kinds and other tenants get not_found.
func TestRulesReadIsTenantScoped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.rules(t, nil)
	got, err := e.svc.GetRules(ctx, e.staffU, e.app)
	if err != nil || got.SpendPerPoint != 100 || !got.Enabled {
		t.Fatalf("staff reads rules = %+v, %v", got, err)
	}
	tenantB := e.d.NewTenant(t, "Beta")
	ownerB := e.user(t, tenantB, "owner")
	member := principal.Principal{Kind: principal.KindMember, MemberID: e.member(t, e.tenant, e.app), AppID: e.app, TenantID: e.tenant}
	for name, p := range map[string]principal.Principal{"tenant B owner": ownerB, "member": member, "staff app": e.staff("manager")} {
		if _, err := e.svc.GetRules(ctx, p, e.app); code(err) != "not_found" {
			t.Errorf("%s: %v, want not_found", name, err)
		}
	}
}

// Invariant: a member's own export drops notes and references, the
// dashboard export keeps them, and both carry every transaction.
func TestExportHidesNotesFromTheMember(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t, e.tenant, e.app)
	e.adjust(t, m, 50)
	e.adjust(t, m, -10)
	err := db.WithTenant(ctx, e.d.App, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		mem, err := access.LoadMember(ctx, q, m, e.app)
		if err != nil {
			return err
		}
		for _, forMember := range []bool{true, false} {
			data, err := points.ExportMember(ctx, q, mem, e.clk.Now(), forMember)
			if err != nil {
				return err
			}
			if len(data.History) != 2 || data.Summary.Balance != 40 {
				t.Errorf("forMember %v: %d rows, balance %d", forMember, len(data.History), data.Summary.Balance)
			}
			for _, tx := range data.History {
				if hidden := tx.Note == nil; hidden != forMember {
					t.Errorf("forMember %v: note %v", forMember, tx.Note)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Invariant: the maintenance job drops single-use scan records older than
// a day and keeps newer ones, across tenants.
func TestDeleteOldScanTokenUses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := e.clk.Now()
	e.d.Exec(t, "INSERT INTO scan_token_uses (jti, tenant_id, app_id, used_at) VALUES ('old', $1, $2, $3), ('new', $1, $2, $4)",
		e.tenant, e.app, now.Add(-25*time.Hour), now.Add(-time.Hour))
	n, err := points.DeleteOldScanTokenUses(ctx, e.d.App, now)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v; want 1", n, err)
	}
	var left string
	if err := e.d.Admin.QueryRow(ctx, "SELECT string_agg(jti, ',') FROM scan_token_uses").Scan(&left); err != nil || left != "new" {
		t.Errorf("left %q, %v", left, err)
	}
}
