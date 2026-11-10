package coupon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

// Invariant (Phase 4 exit criterion): of 1,000 concurrent redeems of one
// coupon exactly one succeeds; every other attempt gets 409
// coupon_unavailable, and the coupon is redeemed once, with one
// coupon.redeemed event.
func TestParallelRedeemsOneWins(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	m := e.member(t)
	inst := e.issue(t, m, e.template(t, TemplateInput{}).ID)
	st := e.staff()
	tok := e.scan(t, st, m)

	const n = 1000
	results := make([]error, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			_, results[i] = e.svc.Redeem(context.Background(), st, inst.ID, idem(), tok)
		}()
	}
	close(release)
	wg.Wait()

	ok := 0
	for i, err := range results {
		switch code(err) {
		case "":
			ok++
		case "coupon_unavailable":
		default:
			t.Fatalf("redeem %d: %v", i, err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d redeems succeeded, want exactly 1", ok)
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE id = $1 AND status = 'redeemed'", inst.ID); got != 1 {
		t.Errorf("redeemed rows = %d", got)
	}
	if got := e.count(t, "SELECT count(*) FROM outbound_events WHERE type = 'coupon.redeemed' AND payload->>'coupon_id' = $1", inst.ID.String()); got != 1 {
		t.Errorf("coupon.redeemed events = %d, want 1", got)
	}
}

// Invariant: concurrent claims never issue more than total_limit coupons
// of a template, nor more than per_member_limit to one member, and
// claimed_count always equals the coupons issued.
func TestParallelClaimsRespectLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	cases := []struct {
		name        string
		total       *int
		perMember   int
		members     int
		perMemberGo int // concurrent claims per member
		wantIssued  int
	}{
		{"total limit binds", intp(20), 2, 30, 3, 20},
		{"per-member limit binds", nil, 3, 5, 20, 15},
		{"both, one member", intp(2), 2, 1, 50, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tmpl := e.template(t, TemplateInput{TotalLimit: tc.total, PerMemberLimit: intp(tc.perMember), Claimable: boolp(true)})
			members := make([]uuid.UUID, tc.members)
			for i := range members {
				members[i] = e.member(t)
			}
			var mu sync.Mutex
			codes := map[string]int{}
			release := make(chan struct{})
			var wg sync.WaitGroup
			for _, m := range members {
				for range tc.perMemberGo {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-release
						_, err := e.svc.Claim(context.Background(), e.memberP(m), tmpl.ID, idem())
						mu.Lock()
						codes[code(err)]++
						mu.Unlock()
					}()
				}
			}
			close(release)
			wg.Wait()

			for c := range codes {
				if c != "" && c != "coupon_sold_out" && c != "coupon_limit_reached" {
					t.Fatalf("unexpected outcome %q (%d times)", c, codes[c])
				}
			}
			if codes[""] != tc.wantIssued {
				t.Errorf("%d claims succeeded, want %d (outcomes %v)", codes[""], tc.wantIssued, codes)
			}
			if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE template_id = $1", tmpl.ID); got != tc.wantIssued {
				t.Errorf("instances = %d, want %d", got, tc.wantIssued)
			}
			if got := e.count(t, "SELECT claimed_count FROM coupon_templates WHERE id = $1", tmpl.ID); got != tc.wantIssued {
				t.Errorf("claimed_count = %d, want %d", got, tc.wantIssued)
			}
			if got := e.count(t, "SELECT coalesce(max(n), 0) FROM (SELECT count(*) AS n FROM coupon_instances WHERE template_id = $1 GROUP BY member_id) c", tmpl.ID); got > tc.perMember {
				t.Errorf("a member holds %d, above per_member_limit %d", got, tc.perMember)
			}
			if got := e.count(t, "SELECT count(*) FROM outbound_events WHERE type = 'coupon.issued' AND payload->>'template_id' = $1", tmpl.ID.String()); got != tc.wantIssued {
				t.Errorf("coupon.issued events = %d, want %d", got, tc.wantIssued)
			}
		})
	}
}

// Invariant: redeeming works until the last microsecond before expires_at
// and fails from expires_at on, by the service clock.
func TestRedeemFailsExactlyAtExpiresAt(t *testing.T) {
	e := newEnv(t)
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{ValidityDays: intp(1)})
	before, at := e.issue(t, m, tmpl.ID), e.issue(t, m, tmpl.ID)
	if before.ExpiresAt == nil || !before.ExpiresAt.Equal(e.clk.Now().Add(24*time.Hour)) {
		t.Fatalf("expires_at = %v, want issue + 1 day", before.ExpiresAt)
	}
	st := e.staff()

	e.clk.Advance(24*time.Hour - time.Microsecond)
	if _, err := e.svc.Redeem(context.Background(), st, before.ID, idem(), e.scan(t, st, m)); err != nil {
		t.Fatalf("redeem a microsecond before expiry: %v", err)
	}
	e.clk.Advance(time.Microsecond)
	if _, err := e.svc.Redeem(context.Background(), st, at.ID, idem(), e.scan(t, st, m)); code(err) != "coupon_unavailable" {
		t.Fatalf("redeem at expires_at: %v, want coupon_unavailable", err)
	}
	page, err := e.svc.ListMember(context.Background(), e.owner, m, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	states := map[uuid.UUID]string{}
	for _, it := range page.Items {
		states[it.ID] = it.State
	}
	if states[before.ID] != StatusRedeemed || states[at.ID] != StateExpired {
		t.Errorf("states = %v", states)
	}
	// A void still works on an expired coupon.
	if _, err := e.svc.Void(context.Background(), e.owner, at.ID, idem(), "expired at the counter"); err != nil {
		t.Errorf("void an expired coupon: %v", err)
	}
}

// Invariant: every redeem refusal is the same 409, whatever the reason, and
// a refused redeem changes nothing.
func TestRedeemRefusalsLookAlike(t *testing.T) {
	e := newEnv(t)
	m, other := e.member(t), e.member(t)
	tmpl := e.template(t, TemplateInput{})
	otherApp := e.newApp(t, e.tenant)
	foreignMember := memberIn(t, e, e.tenant, otherApp)
	foreignTmpl, err := e.svc.CreateTemplate(context.Background(), e.owner, otherApp, idem(), TemplateInput{Name: "x", Type: TypeFreeItem})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := e.svc.Issue(context.Background(), e.owner, foreignMember, foreignTmpl.ID, idem())
	if err != nil {
		t.Fatal(err)
	}
	redeemed, voided, othersCoupon := e.issue(t, m, tmpl.ID), e.issue(t, m, tmpl.ID), e.issue(t, other, tmpl.ID)
	st := e.staff()
	if _, err := e.svc.Redeem(context.Background(), st, redeemed.ID, idem(), e.scan(t, st, m)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Void(context.Background(), e.owner, voided.ID, idem(), "test"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		id   uuid.UUID
	}{
		{"unknown", uuid.New()},
		{"already redeemed", redeemed.ID},
		{"voided", voided.ID},
		{"another member's", othersCoupon.ID},
		{"another app's", foreign.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.svc.Redeem(context.Background(), st, tc.id, idem(), e.scan(t, st, m)); code(err) != "coupon_unavailable" {
				t.Fatalf("redeem: %v, want coupon_unavailable", err)
			}
		})
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE status = 'redeemed'"); got != 1 {
		t.Errorf("redeemed coupons = %d, want 1", got)
	}
}

// Invariant: a write with an Idempotency-Key runs once; the same key and
// payload replays the stored coupon, a different payload is 422, and a
// replayed redeem works even after the scan expired.
func TestIdempotentReplay(t *testing.T) {
	e := newEnv(t)
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{Claimable: boolp(true), PerMemberLimit: intp(5)})
	ctx := context.Background()

	k := idem()
	first, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, k)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, k)
	if err != nil || again.ID != first.ID || again.Code != first.Code {
		t.Fatalf("replay = %+v, %v; want %+v", again, err, first)
	}
	if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, Idem{Key: k.Key, Hash: []byte("other")}); code(err) != "idempotency_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1", m); got != 1 {
		t.Errorf("instances = %d, want 1", got)
	}

	st := e.staff()
	rk := idem()
	tok := e.scan(t, st, m)
	red, err := e.svc.Redeem(ctx, st, first.ID, rk, tok)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(10 * time.Minute) // the scan token is long expired
	replay, err := e.svc.Redeem(ctx, st, first.ID, rk, tok)
	if err != nil || replay.ID != red.ID || replay.State != StatusRedeemed {
		t.Fatalf("late replay = %+v, %v", replay, err)
	}
	if _, err := e.svc.Redeem(ctx, st, first.ID, idem(), tok); code(err) != "scan_invalid" {
		t.Fatalf("new redeem with the expired scan: %v, want scan_invalid", err)
	}
	if got := e.count(t, "SELECT count(*) FROM outbound_events WHERE type = 'coupon.redeemed'"); got != 1 {
		t.Errorf("coupon.redeemed events = %d, want 1", got)
	}
}

// Invariant: staff cannot redeem with another staff member's scan, nor on
// their own member card.
func TestRedeemScanBinding(t *testing.T) {
	e := newEnv(t)
	m := e.member(t)
	inst := e.issue(t, m, e.template(t, TemplateInput{}).ID)
	st, other := e.staff(), e.staff()
	if _, err := e.svc.Redeem(context.Background(), other, inst.ID, idem(), e.scan(t, st, m)); code(err) != "scan_mismatch" {
		t.Fatalf("another staff member's scan: %v", err)
	}
	if _, err := e.svc.Redeem(context.Background(), st, inst.ID, idem(), "st1.garbage.sig"); code(err) != "scan_invalid" {
		t.Fatalf("garbage scan: %v", err)
	}
	// A staff row bound to the member's LINE account is the member's own card.
	self := e.staff()
	e.d.Exec(t, `INSERT INTO staff (id, tenant_id, app_id, display_name, role, line_user_id) VALUES ($1, $2, $3, 'Self', 'cashier', $4)`,
		self.StaffID, e.tenant, e.app, "U"+m.String())
	if _, err := e.svc.Redeem(context.Background(), self, inst.ID, idem(), e.scan(t, self, m)); code(err) != "self_service" {
		t.Fatalf("own card: %v", err)
	}
	// An erased member's scan names nobody any more.
	e.d.Exec(t, "UPDATE members SET anonymized_at = now() WHERE id = $1", m)
	if _, err := e.svc.Redeem(context.Background(), st, inst.ID, idem(), e.scan(t, st, m)); code(err) != "scan_invalid" {
		t.Fatalf("erased member: %v", err)
	}
}

// Invariant: dashboard and API issues ignore claimable and
// per_member_limit but respect total_limit and the window; claims respect
// all of them.
func TestIssueRules(t *testing.T) {
	e := newEnv(t)
	m := e.member(t)
	ctx := context.Background()
	future := e.clk.Now().Add(time.Hour)
	past := e.clk.Now().Add(-time.Hour)
	cases := []struct {
		name         string
		in           TemplateInput
		claim, iss   string // expected codes for a claim and for a dashboard issue
		archiveFirst bool
	}{
		{"issue-only template", TemplateInput{}, "coupon_not_claimable", "", false},
		{"claimable", TemplateInput{Claimable: boolp(true)}, "", "", false},
		{"not yet valid", TemplateInput{Claimable: boolp(true), ValidFrom: &future}, "coupon_not_claimable", "coupon_not_claimable", false},
		{"window over", TemplateInput{Claimable: boolp(true), ValidFrom: ptime(past.Add(-time.Hour)), ValidTo: &past}, "coupon_not_claimable", "coupon_not_claimable", false},
		{"archived", TemplateInput{Claimable: boolp(true)}, "coupon_not_claimable", "coupon_not_claimable", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := e.template(t, tc.in)
			if tc.archiveFirst {
				if _, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{Status: strp("archived")}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem()); code(err) != tc.claim {
				t.Errorf("claim: %v, want %q", err, tc.claim)
			}
			if _, err := e.svc.Issue(ctx, e.owner, m, tmpl.ID, idem()); code(err) != tc.iss {
				t.Errorf("issue: %v, want %q", err, tc.iss)
			}
		})
	}

	t.Run("issue ignores per_member_limit, not total_limit", func(t *testing.T) {
		tmpl := e.template(t, TemplateInput{TotalLimit: intp(2), Claimable: boolp(true)})
		if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem()); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem()); code(err) != "coupon_limit_reached" {
			t.Fatalf("second claim: %v", err)
		}
		if _, err := e.svc.Issue(ctx, e.owner, m, tmpl.ID, idem()); err != nil {
			t.Fatalf("issue past per_member_limit: %v", err)
		}
		if _, err := e.svc.Issue(ctx, e.owner, m, tmpl.ID, idem()); code(err) != "coupon_sold_out" {
			t.Fatalf("issue past total_limit: %v", err)
		}
	})

	t.Run("a void frees the member's claim but not total_limit", func(t *testing.T) {
		tmpl := e.template(t, TemplateInput{TotalLimit: intp(2), Claimable: boolp(true)})
		c, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Void(ctx, e.owner, c.ID, idem(), "mistake"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, idem()); err != nil {
			t.Fatalf("claim after void: %v", err)
		}
		if _, err := e.svc.Claim(ctx, e.memberP(e.member(t)), tmpl.ID, idem()); code(err) != "coupon_sold_out" {
			t.Fatalf("third claim: %v, want sold out (voids do not give stock back)", err)
		}
		offers, err := e.svc.Offers(ctx, e.memberP(m))
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range offers {
			if o.Template.ID == tmpl.ID {
				t.Errorf("sold-out template still offered: %+v", o)
			}
		}
	})
}

func ptime(t time.Time) *time.Time { return &t }

// Invariant: type and value are fixed once a coupon was issued, and
// total_limit cannot drop below claimed_count (422 validation_failed with
// details.total_limit); before the first issue both may change.
func TestUpdateTemplateRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	free := TypeFreeItem
	pct := TypePercentOff

	tmpl := e.template(t, TemplateInput{TotalLimit: intp(5)})
	got, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{Type: &free})
	if err != nil || got.Type != TypeFreeItem || got.Value != nil {
		t.Fatalf("switch to free_item before any issue: %+v, %v", got, err)
	}
	if _, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{Type: &pct}); code(err) != "validation_failed" {
		t.Fatalf("percent_off without a value: %v", err)
	}
	if _, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{Type: &pct, Value: intp(10)}); err != nil {
		t.Fatalf("switch to percent_off: %v", err)
	}
	m := e.member(t)
	e.issue(t, m, tmpl.ID)
	e.issue(t, m, tmpl.ID)

	cases := []struct {
		name   string
		in     TemplateUpdate
		code   string
		status int
	}{
		{"value after an issue", TemplateUpdate{Value: intp(20)}, "coupon_template_in_use", 409},
		{"type after an issue", TemplateUpdate{Type: &free}, "coupon_template_in_use", 409},
		{"same type and value", TemplateUpdate{Type: &pct, Value: intp(10)}, "", 0},
		{"total limit below claimed", TemplateUpdate{TotalLimit: intp(1)}, "validation_failed", 422},
		{"total limit at claimed", TemplateUpdate{TotalLimit: intp(2), PerMemberLimit: intp(1)}, "", 0},
		{"per member above total", TemplateUpdate{PerMemberLimit: intp(3)}, "validation_failed", 400},
		{"send and clear", TemplateUpdate{TotalLimit: intp(9), Clear: []string{"total_limit"}}, "validation_failed", 400},
		{"clear unknown", TemplateUpdate{Clear: []string{"name"}}, "validation_failed", 400},
		{"archive", TemplateUpdate{Status: strp("archived")}, "", 0},
		{"reactivate", TemplateUpdate{Status: strp("active")}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), tc.in)
			if code(err) != tc.code {
				t.Fatalf("update: %v, want %q", err, tc.code)
			}
			if tc.status != 0 {
				if got := statusOf(err); got != tc.status {
					t.Fatalf("status = %d, want %d (%v)", got, tc.status, err)
				}
			}
			if tc.name == "total limit below claimed" {
				if d := detailsOf(err); d["total_limit"] == nil {
					t.Errorf("details = %v, want total_limit", d)
				}
			}
		})
	}
}

// Invariant: an outbound event is written in the transaction of the change
// it describes, so a write that fails after the event was emitted leaves
// neither the coupon nor the event behind.
func TestEventsRollBackWithTheWrite(t *testing.T) {
	e := newEnv(t)
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{})
	errLater := errors.New("a later step failed")
	err := db.WithTenant(context.Background(), e.d.App, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		mem, err := access.LoadMember(ctx, sqlc.New(tx), m, e.app)
		if err != nil {
			return err
		}
		if _, err := issue(ctx, tx, mem, tmpl.ID, issuer{source: SourceIssue, actorType: "system"}, e.clk.Now()); err != nil {
			return err
		}
		if n := e.countTx(t, tx, "SELECT count(*) FROM outbound_events WHERE type = 'coupon.issued'"); n != 1 {
			t.Errorf("inside the transaction: %d coupon.issued events, want 1", n)
		}
		return errLater
	})
	if !errors.Is(err, errLater) {
		t.Fatalf("transaction: %v", err)
	}
	for q, want := range map[string]int{
		"SELECT count(*) FROM outbound_events":                                             0,
		"SELECT count(*) FROM coupon_instances":                                            0,
		"SELECT claimed_count FROM coupon_templates WHERE id = '" + tmpl.ID.String() + "'": 0,
	} {
		if got := e.count(t, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	// And a committed issue writes exactly one event with IDs only.
	inst := e.issue(t, m, tmpl.ID)
	var payload map[string]any
	if err := e.d.Admin.QueryRow(context.Background(), "SELECT payload FROM outbound_events WHERE type = 'coupon.issued'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["coupon_id"] != inst.ID.String() || payload["member_id"] != m.String() || payload["source"] != SourceIssue {
		t.Errorf("payload = %v", payload)
	}
}

func (e *env) countTx(t *testing.T, tx pgx.Tx, q string) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// Invariant: coupons of one tenant are invisible to another tenant's
// users and members, for reads and writes.
func TestCrossTenantCoupons(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{Claimable: boolp(true)})
	inst := e.issue(t, m, tmpl.ID)

	tenantB := e.d.NewTenant(t, "Beta")
	appB := e.newApp(t, tenantB)
	ownerB := e.user(t, tenantB, "owner")
	memberB := memberIn(t, e, tenantB, appB)
	pB := e.memberP(memberB)
	pB.AppID, pB.TenantID = appB, tenantB

	checks := []struct {
		name string
		call func() error
	}{
		{"get template", func() error { _, err := e.svc.GetTemplate(ctx, ownerB, tmpl.ID); return err }},
		{"update template", func() error {
			_, err := e.svc.UpdateTemplate(ctx, ownerB, tmpl.ID, idem(), TemplateUpdate{Name: strp("x")})
			return err
		}},
		{"list templates", func() error { _, err := e.svc.ListTemplates(ctx, ownerB, e.app, nil); return err }},
		{"list member coupons", func() error { _, err := e.svc.ListMember(ctx, ownerB, m, nil, 0, ""); return err }},
		{"issue to member", func() error { _, err := e.svc.Issue(ctx, ownerB, m, tmpl.ID, idem()); return err }},
		{"void", func() error { _, err := e.svc.Void(ctx, ownerB, inst.ID, idem(), "x"); return err }},
		{"member B claims A's template", func() error { _, err := e.svc.Claim(ctx, pB, tmpl.ID, idem()); return err }},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); code(err) != "not_found" {
				t.Fatalf("%v, want not_found", err)
			}
		})
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances"); got != 1 {
		t.Errorf("instances = %d, want 1", got)
	}
}

func memberIn(t *testing.T, e *env, tenantID, appID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	e.d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", id, tenantID, appID, "U"+id.String())
	return id
}

// Invariant: the member's own list hides voided coupons and maps used to
// redeemed; pages are newest first and complete.
func TestListsAndPaging(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{})
	var ids []uuid.UUID
	for range 7 {
		ids = append(ids, e.issue(t, m, tmpl.ID).ID)
		e.clk.Advance(time.Second)
	}
	st := e.staff()
	if _, err := e.svc.Redeem(ctx, st, ids[0], idem(), e.scan(t, st, m)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Void(ctx, e.owner, ids[1], idem(), "x"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		list  func(state *string, cursor string) (Page, error)
		state *string
		want  int
	}{
		{"member, all", func(s *string, c string) (Page, error) { return e.svc.ListMine(ctx, e.memberP(m), s, 2, c) }, nil, 6},
		{"member, used", func(s *string, c string) (Page, error) { return e.svc.ListMine(ctx, e.memberP(m), s, 2, c) }, strp("used"), 1},
		{"member, available", func(s *string, c string) (Page, error) { return e.svc.ListMine(ctx, e.memberP(m), s, 2, c) }, strp("available"), 5},
		{"dashboard, all", func(s *string, c string) (Page, error) { return e.svc.ListMember(ctx, e.owner, m, s, 2, c) }, nil, 7},
		{"dashboard, voided", func(s *string, c string) (Page, error) { return e.svc.ListMember(ctx, e.owner, m, s, 2, c) }, strp("voided"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := 0
			cursor := ""
			var last time.Time
			for {
				p, err := tc.list(tc.state, cursor)
				if err != nil {
					t.Fatal(err)
				}
				for _, it := range p.Items {
					if !last.IsZero() && it.IssuedAt.After(last) {
						t.Fatalf("not newest first")
					}
					last = it.IssuedAt
					if tc.name == "member, all" && it.Status == StatusVoided {
						t.Fatalf("member sees a voided coupon")
					}
				}
				seen += len(p.Items)
				if p.NextCursor == "" {
					break
				}
				cursor = p.NextCursor
			}
			if seen != tc.want {
				t.Fatalf("listed %d, want %d", seen, tc.want)
			}
		})
	}
	if _, err := e.svc.ListMine(ctx, e.memberP(m), strp("voided"), 0, ""); code(err) != "bad_request" {
		t.Errorf("member voided filter: %v", err)
	}
}
