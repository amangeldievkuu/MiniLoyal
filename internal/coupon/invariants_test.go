package coupon

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Test-engineer additions for Phase 4: races between different writes on
// one coupon or template, idempotency under concurrency and after a change
// of authorization, and the principals the behaviour tests do not use
// (API keys, staff of another tenant).

func (e *env) apiKey(tenantID, appID uuid.UUID) principal.Principal {
	return principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: tenantID, AppID: appID,
		Scopes: []string{"members:read", "coupons:write"}}
}

// run starts n goroutines that wait on one channel and then call fn(i).
func run(n int, fn func(i int)) {
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			fn(i)
		}()
	}
	close(release)
	wg.Wait()
}

// Invariant: a redeem and a void of one coupon are both conditional
// updates on status 'issued', so of 200 concurrent redeems and voids
// exactly one succeeds, the stored status is the winner's, and a
// coupon.redeemed event exists exactly when the redeem won.
func TestRedeemAndVoidRaceExactlyOneWins(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	m := e.member(t)
	inst := e.issue(t, m, e.template(t, TemplateInput{}).ID)
	st := e.staff()
	tok := e.scan(t, st, m)

	const n = 200
	type outcome struct {
		redeem bool
		code   string
	}
	results := make([]outcome, n)
	run(n, func(i int) {
		var err error
		if i%2 == 0 {
			_, err = e.svc.Redeem(context.Background(), st, inst.ID, idem(), tok)
		} else {
			_, err = e.svc.Void(context.Background(), e.owner, inst.ID, idem(), "race")
		}
		results[i] = outcome{redeem: i%2 == 0, code: code(err)}
	})

	wins, redeemWon := 0, false
	for i, r := range results {
		switch r.code {
		case "":
			wins++
			redeemWon = r.redeem
		case "coupon_unavailable":
		default:
			t.Fatalf("call %d (redeem %v): %s", i, r.redeem, r.code)
		}
	}
	if wins != 1 {
		t.Fatalf("%d of %d redeems and voids succeeded, want exactly 1", wins, n)
	}
	want := StatusVoided
	if redeemWon {
		want = StatusRedeemed
	}
	var status string
	if err := e.d.Admin.QueryRow(context.Background(), "SELECT status FROM coupon_instances WHERE id = $1", inst.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Errorf("status = %s, want %s (the winner's)", status, want)
	}
	wantEvents := 0
	if redeemWon {
		wantEvents = 1
	}
	if got := e.count(t, "SELECT count(*) FROM outbound_events WHERE type = 'coupon.redeemed'"); got != wantEvents {
		t.Errorf("coupon.redeemed events = %d, want %d", got, wantEvents)
	}
	audits := e.count(t, "SELECT count(*) FROM audit_log WHERE action IN ('coupon.redeem', 'coupon.void')")
	if audits != 1 {
		t.Errorf("redeem and void audit rows = %d, want 1", audits)
	}
}

// Invariant: total_limit holds across every issuing path at once. 200
// concurrent claims, dashboard issues and API-key issues of one template
// never issue more than total_limit coupons; claimed_count equals the rows
// and the coupon.issued events.
func TestMixedIssuersNeverPassTotalLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	const limit, n = 25, 200
	tmpl := e.template(t, TemplateInput{TotalLimit: intp(limit), PerMemberLimit: intp(1), Claimable: boolp(true)})
	key := e.apiKey(e.tenant, e.app)
	members := make([]uuid.UUID, n)
	for i := range members {
		members[i] = e.member(t)
	}
	codes := make([]string, n)
	run(n, func(i int) {
		var err error
		switch i % 3 {
		case 0:
			_, err = e.svc.Claim(context.Background(), e.memberP(members[i]), tmpl.ID, idem())
		case 1:
			_, err = e.svc.Issue(context.Background(), e.owner, members[i], tmpl.ID, idem())
		default:
			_, err = e.svc.Issue(context.Background(), key, members[i], tmpl.ID, idem())
		}
		codes[i] = code(err)
	})
	ok := 0
	for i, c := range codes {
		switch c {
		case "":
			ok++
		case "coupon_sold_out":
		default:
			t.Fatalf("issue %d: %s", i, c)
		}
	}
	if ok != limit {
		t.Errorf("%d issues succeeded, want total_limit %d", ok, limit)
	}
	for q, want := range map[string]int{
		"SELECT count(*) FROM coupon_instances WHERE template_id = $1":                                             limit,
		"SELECT claimed_count FROM coupon_templates WHERE id = $1":                                                 limit,
		"SELECT count(*) FROM outbound_events WHERE type = 'coupon.issued' AND payload->>'template_id' = $1::text": limit,
	} {
		if got := e.count(t, q, tmpl.ID); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}

// Invariant: lowering total_limit while claims run never leaves more
// coupons issued than the limit the template ends with: the update and the
// claims serialize on the template row, and an update below claimed_count
// is refused.
func TestTotalLimitUpdateRacingClaims(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	const n = 200
	tmpl := e.template(t, TemplateInput{TotalLimit: intp(150), Claimable: boolp(true)})
	members := make([]uuid.UUID, n)
	for i := range members {
		members[i] = e.member(t)
	}
	var updated sync.Map
	run(n, func(i int) {
		if i%20 == 0 {
			// Ten updates try limits from 10 to 100.
			lim := 10 + i/2
			if _, err := e.svc.UpdateTemplate(context.Background(), e.owner, tmpl.ID, idem(), TemplateUpdate{TotalLimit: intp(lim)}); err == nil {
				updated.Store(lim, true)
			} else if code(err) != "validation_failed" {
				t.Errorf("update to %d: %v", lim, err)
			}
			return
		}
		if _, err := e.svc.Claim(context.Background(), e.memberP(members[i]), tmpl.ID, idem()); err != nil && code(err) != "coupon_sold_out" {
			t.Errorf("claim %d: %v", i, err)
		}
	})
	issued := e.count(t, "SELECT count(*) FROM coupon_instances WHERE template_id = $1", tmpl.ID)
	limit := e.count(t, "SELECT total_limit FROM coupon_templates WHERE id = $1", tmpl.ID)
	claimed := e.count(t, "SELECT claimed_count FROM coupon_templates WHERE id = $1", tmpl.ID)
	if issued > limit || claimed != issued {
		t.Fatalf("issued %d, claimed_count %d, total_limit %d", issued, claimed, limit)
	}
}

// Invariant: 200 concurrent claims with one Idempotency-Key and one payload
// issue one coupon; every caller that gets an answer gets that coupon.
func TestSameKeyConcurrentClaimsIssueOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{Claimable: boolp(true), PerMemberLimit: intp(50)})
	k := idem()
	const n = 200
	got := make([]Instance, n)
	codes := make([]string, n)
	run(n, func(i int) {
		var err error
		got[i], err = e.svc.Claim(context.Background(), e.memberP(m), tmpl.ID, k)
		codes[i] = code(err)
	})
	var first *Instance
	for i := range n {
		switch codes[i] {
		case "":
			if first == nil {
				first = &got[i]
			} else if got[i].ID != first.ID || got[i].Code != first.Code {
				t.Fatalf("answer %d is coupon %s, another is %s", i, got[i].ID, first.ID)
			}
		case "conflict":
			// The in-flight answer is allowed; the work still ran once.
		default:
			t.Fatalf("claim %d: %s", i, codes[i])
		}
	}
	if first == nil {
		t.Fatal("no claim succeeded")
	}
	if c := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1", m); c != 1 {
		t.Errorf("instances = %d, want 1", c)
	}
	if c := e.count(t, "SELECT count(*) FROM idempotency_keys WHERE key = $1", k.Key); c != 1 {
		t.Errorf("idempotency rows = %d, want 1", c)
	}
}

// Invariant: authorization runs before a replay. A dashboard user demoted
// below admin, or a member who was erased, cannot read a stored outcome by
// replaying the key.
func TestReplayIsReauthorized(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{Claimable: boolp(true)})
	admin := e.user(t, e.tenant, "admin")

	ik := idem()
	issued, err := e.svc.Issue(ctx, admin, m, tmpl.ID, ik)
	if err != nil {
		t.Fatal(err)
	}
	vk := idem()
	if _, err := e.svc.Void(ctx, admin, issued.ID, vk, "mistake"); err != nil {
		t.Fatal(err)
	}
	e.d.Exec(t, "UPDATE tenant_users SET role = 'staff' WHERE user_id = $1", admin.UserID)
	if inst, err := e.svc.Issue(ctx, admin, m, tmpl.ID, ik); err == nil || inst.ID == issued.ID {
		t.Errorf("demoted issue replay = %+v, %v; want refused", inst, err)
	}
	if inst, err := e.svc.Void(ctx, admin, issued.ID, vk, "mistake"); err == nil || inst.ID == issued.ID {
		t.Errorf("demoted void replay = %+v, %v; want refused", inst, err)
	}
	e.d.Exec(t, "DELETE FROM tenant_users WHERE user_id = $1", admin.UserID)
	if _, err := e.svc.Issue(ctx, admin, m, tmpl.ID, ik); code(err) != "not_found" {
		t.Errorf("removed user's replay: %v, want not_found", err)
	}

	ck := idem()
	if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, ck); err != nil {
		t.Fatal(err)
	}
	e.d.Exec(t, "UPDATE members SET anonymized_at = now() WHERE id = $1", m)
	if _, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, ck); code(err) != "not_found" {
		t.Errorf("erased member's replay: %v, want not_found", err)
	}
}

// Invariant: an Idempotency-Key belongs to one principal. The same key and
// payload from another user of the tenant, or from a user of another
// tenant, runs as a new request and never returns the first caller's
// coupon.
func TestIdempotencyKeyIsScopedToThePrincipal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{})
	k := idem()
	first, err := e.svc.Issue(ctx, e.owner, m, tmpl.ID, k)
	if err != nil {
		t.Fatal(err)
	}

	admin := e.user(t, e.tenant, "admin")
	second, err := e.svc.Issue(ctx, admin, m, tmpl.ID, k)
	if err != nil || second.ID == first.ID {
		t.Fatalf("another admin with the same key = %+v, %v; want a new coupon", second, err)
	}

	tenantB := e.d.NewTenant(t, "Beta")
	appB := e.newApp(t, tenantB)
	ownerB := e.user(t, tenantB, "owner")
	memberB := memberIn(t, e, tenantB, appB)
	tmplB, err := e.svc.CreateTemplate(ctx, ownerB, appB, idem(), TemplateInput{Name: "B coupon", Type: TypeFreeItem})
	if err != nil {
		t.Fatal(err)
	}
	third, err := e.svc.Issue(ctx, ownerB, memberB, tmplB.ID, k)
	if err != nil || third.ID == first.ID || third.MemberID != memberB {
		t.Fatalf("tenant B with A's key = %+v, %v; want B's own coupon", third, err)
	}
	// The same key reused across a tenant boundary with A's IDs leaks
	// nothing either.
	if inst, err := e.svc.Issue(ctx, ownerB, m, tmpl.ID, k); code(err) != "not_found" || inst.ID != uuid.Nil {
		t.Errorf("tenant B replaying A's request = %+v, %v; want not_found", inst, err)
	}
}

// Invariant: API keys stay inside their own app and tenant, and cannot use
// dashboard-only operations; staff of another tenant cannot redeem.
func TestCrossTenantCouponsForAPIKeysAndStaff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{Claimable: boolp(true)})
	inst := e.issue(t, m, tmpl.ID)

	tenantB := e.d.NewTenant(t, "Beta")
	appB := e.newApp(t, tenantB)
	keyB := e.apiKey(tenantB, appB)
	annex := e.newApp(t, e.tenant)
	keyAnnex := e.apiKey(e.tenant, annex)
	keyA := e.apiKey(e.tenant, e.app)

	checks := []struct {
		name string
		call func() error
	}{
		{"tenant B key lists A's member", func() error { _, err := e.svc.ListMember(ctx, keyB, m, nil, 0, ""); return err }},
		{"tenant B key issues to A's member", func() error { _, err := e.svc.Issue(ctx, keyB, m, tmpl.ID, idem()); return err }},
		{"other app's key lists the member", func() error { _, err := e.svc.ListMember(ctx, keyAnnex, m, nil, 0, ""); return err }},
		{"other app's key issues to the member", func() error { _, err := e.svc.Issue(ctx, keyAnnex, m, tmpl.ID, idem()); return err }},
		{"key voids", func() error { _, err := e.svc.Void(ctx, keyA, inst.ID, idem(), "x"); return err }},
		{"key reads a template", func() error { _, err := e.svc.GetTemplate(ctx, keyA, tmpl.ID); return err }},
		{"key updates a template", func() error {
			_, err := e.svc.UpdateTemplate(ctx, keyA, tmpl.ID, idem(), TemplateUpdate{Name: strp("x")})
			return err
		}},
		{"member issues", func() error { _, err := e.svc.Issue(ctx, e.memberP(m), m, tmpl.ID, idem()); return err }},
		{"member voids", func() error { _, err := e.svc.Void(ctx, e.memberP(m), inst.ID, idem(), "x"); return err }},
		{"member lists through the dashboard", func() error { _, err := e.svc.ListMember(ctx, e.memberP(m), m, nil, 0, ""); return err }},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if s := statusOf(err); s != 404 && s != 403 {
				t.Fatalf("%v, want 404 or 403", err)
			}
		})
	}
	// The key of the member's own app does reach it.
	if _, err := e.svc.Issue(ctx, keyA, m, tmpl.ID, idem()); err != nil {
		t.Fatalf("own app key: %v", err)
	}

	// Staff of tenant B, with a scan naming A's member under B's app: the
	// member is not in B's app, so the scan names nobody.
	stB := principal.Principal{Kind: principal.KindStaff, StaffID: testutil.NewID(), StaffRole: "cashier", AppID: appB, TenantID: tenantB}
	if _, err := e.svc.Redeem(ctx, stB, inst.ID, idem(), e.scan(t, stB, m)); code(err) != "scan_invalid" {
		t.Errorf("staff of tenant B: %v, want scan_invalid", err)
	}
	// And a scan issued for A's staff is bound to them.
	stA := e.staff()
	if _, err := e.svc.Redeem(ctx, stB, inst.ID, idem(), e.scan(t, stA, m)); code(err) != "scan_mismatch" {
		t.Errorf("staff of tenant B with A's scan: %v, want scan_mismatch", err)
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE status <> 'issued'"); got != 0 {
		t.Errorf("%d coupons changed status", got)
	}
}

// Invariant: a scan token that was tampered with, signed with another key,
// made under a rotated card key, or expired before a first write is
// refused, and the coupon stays issued.
func TestRedeemRefusesBadScans(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	inst := e.issue(t, m, e.template(t, TemplateInput{}).ID)
	st := e.staff()

	good := e.scan(t, st, m)
	other, err := card.New(bytes.Repeat([]byte{9}, 32), e.clk)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := other.IssueScanToken(st.StaffID, m, st.AppID, st.TenantID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rotated, _, err := e.codec.IssueScanToken(st.StaffID, m, st.AppID, st.TenantID, 0)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, token, code string
		before            func()
	}{
		{"empty", "", "scan_invalid", nil},
		{"tampered payload", tamper(good, 0), "scan_invalid", nil},
		{"tampered signature", tamper(good, 1), "scan_invalid", nil},
		{"truncated", good[:len(good)-4], "scan_invalid", nil},
		{"another signing key", foreign, "scan_invalid", nil},
		{"old card key version", rotated, "scan_invalid", nil},
		{"expired before the first write", good, "scan_invalid", func() { e.clk.Advance(10 * time.Minute) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.before != nil {
				tc.before()
			}
			if _, err := e.svc.Redeem(ctx, st, inst.ID, idem(), tc.token); code(err) != tc.code {
				t.Fatalf("redeem: %v, want %s", err, tc.code)
			}
		})
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE status = 'issued'"); got != 1 {
		t.Errorf("issued coupons = %d, want 1", got)
	}
	if got := e.count(t, "SELECT count(*) FROM outbound_events WHERE type = 'coupon.redeemed'"); got != 0 {
		t.Errorf("coupon.redeemed events = %d, want 0", got)
	}
}

// tamper flips one character in the given dot-separated part of a token,
// counted from the end (0 is the last-but-one part, 1 the last).
func tamper(tok string, fromEnd int) string {
	parts := strings.Split(tok, ".")
	i := len(parts) - 2 + fromEnd
	b := []byte(parts[i])
	j := len(b) / 2
	if b[j] == 'A' {
		b[j] = 'B'
	} else {
		b[j] = 'A'
	}
	parts[i] = string(b)
	return strings.Join(parts, ".")
}

// Invariant: a replayed coupon is byte-for-byte the stored outcome: the
// same document a fresh call returned, whatever happened to the coupon
// since (here it was redeemed after the claim).
func TestReplayReturnsTheStoredDocument(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	tmpl := e.template(t, TemplateInput{Claimable: boolp(true)})
	k := idem()
	first, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, k)
	if err != nil {
		t.Fatal(err)
	}
	st := e.staff()
	if _, err := e.svc.Redeem(ctx, st, first.ID, idem(), e.scan(t, st, m)); err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.Claim(ctx, e.memberP(m), tmpl.ID, k)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Errorf("replay differs from the first answer:\n first %s\nreplay %s", a, b)
	}
}

// Invariant: staff see only what they can redeem now (issued and not
// expired), soonest expiry first and never-expiring last; a data-rights
// export lists every coupon, voided ones included, and nothing of another
// member.
func TestRedeemableAndExportReadsStayWithTheMember(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m, other := e.member(t), e.member(t)
	forever := e.template(t, TemplateInput{Name: "Forever"})
	week := e.template(t, TemplateInput{Name: "Week", ValidityDays: intp(7)})
	day := e.template(t, TemplateInput{Name: "Day", ValidityDays: intp(1)})
	keep := e.issue(t, m, forever.ID)
	soon := e.issue(t, m, day.ID)
	later := e.issue(t, m, week.ID)
	voided := e.issue(t, m, forever.ID)
	redeemed := e.issue(t, m, forever.ID)
	e.issue(t, other, forever.ID)
	if _, err := e.svc.Void(ctx, e.owner, voided.ID, idem(), "x"); err != nil {
		t.Fatal(err)
	}
	st := e.staff()
	if _, err := e.svc.Redeem(ctx, st, redeemed.ID, idem(), e.scan(t, st, m)); err != nil {
		t.Fatal(err)
	}

	err := db.WithTenant(ctx, e.d.App, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		mem, err := access.LoadMember(ctx, q, m, e.app)
		if err != nil {
			return err
		}
		got, err := Redeemable(ctx, q, mem, e.clk.Now())
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for _, it := range got {
			ids = append(ids, it.ID)
			if it.MemberID != m || it.State != StateAvailable {
				t.Errorf("redeemable item %+v", it)
			}
		}
		if want := []uuid.UUID{soon.ID, later.ID, keep.ID}; !slices.Equal(ids, want) {
			t.Errorf("redeemable = %v, want %v (soonest expiry first)", ids, want)
		}
		// A day later the one-day coupon is gone from the list.
		got, err = Redeemable(ctx, q, mem, e.clk.Now().Add(24*time.Hour))
		if err != nil {
			return err
		}
		if len(got) != 2 {
			t.Errorf("redeemable after a day = %d, want 2", len(got))
		}

		all, err := ExportMember(ctx, q, mem, e.clk.Now())
		if err != nil {
			return err
		}
		if len(all) != 5 {
			t.Errorf("export lists %d coupons, want 5", len(all))
		}
		for _, it := range all {
			if it.MemberID != m {
				t.Errorf("export lists another member's coupon %+v", it)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Invariant: every team member reads templates; the status filter narrows
// the list; a bad filter is 400; a template of another app is not found
// through this app.
func TestTemplateReads(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	staffU := e.user(t, e.tenant, "staff")
	active := e.template(t, TemplateInput{Name: "Active"})
	archived := e.template(t, TemplateInput{Name: "Old"})
	if _, err := e.svc.UpdateTemplate(ctx, e.owner, archived.ID, idem(), TemplateUpdate{Status: strp("archived")}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		status *string
		want   int
		code   string
	}{
		{nil, 2, ""},
		{strp("active"), 1, ""},
		{strp("archived"), 1, ""},
		{strp("deleted"), 0, "bad_request"},
	}
	for _, tc := range cases {
		got, err := e.svc.ListTemplates(ctx, staffU, e.app, tc.status)
		if code(err) != tc.code || len(got) != tc.want {
			t.Errorf("list %v: %d templates, %v; want %d, %q", tc.status, len(got), err, tc.want, tc.code)
		}
	}
	got, err := e.svc.GetTemplate(ctx, staffU, active.ID)
	if err != nil || got.ID != active.ID || got.Name != "Active" {
		t.Errorf("staff get = %+v, %v", got, err)
	}
	if _, err := e.svc.GetTemplate(ctx, e.owner, uuid.New()); code(err) != "not_found" {
		t.Errorf("unknown template: %v", err)
	}
	if _, err := e.svc.ListTemplates(ctx, e.owner, uuid.New(), nil); code(err) != "not_found" {
		t.Errorf("unknown app: %v", err)
	}
	if _, err := e.svc.CreateTemplate(ctx, staffU, e.app, idem(), TemplateInput{Name: "x", Type: TypeFreeItem}); code(err) != "forbidden" && code(err) != "not_found" {
		t.Errorf("staff creates a template: %v", err)
	}
}

// Invariant: a stamp reward ignores claimable and per_member_limit, shares
// the stamp operation's txn_id, and when the template refuses (archived,
// sold out) it writes nothing and reports ok false; an unknown template is
// an error, not a silent skip.
func TestIssueRewardRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	avail := e.template(t, TemplateInput{PerMemberLimit: intp(1)})
	soldOut := e.template(t, TemplateInput{TotalLimit: intp(1)})
	e.issue(t, e.member(t), soldOut.ID)
	archived := e.template(t, TemplateInput{})
	if _, err := e.svc.UpdateTemplate(ctx, e.owner, archived.ID, idem(), TemplateUpdate{Status: strp("archived")}); err != nil {
		t.Fatal(err)
	}
	txnID := uuid.Must(uuid.NewV7())
	err := db.WithTenant(ctx, e.d.App, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		mem, err := access.LoadMember(ctx, sqlc.New(tx), m, e.app)
		if err != nil {
			return err
		}
		for i := range 2 { // twice: per_member_limit does not apply
			inst, ok, err := IssueReward(ctx, tx, mem, avail.ID, "staff", uuid.Nil, txnID, e.clk.Now())
			if err != nil || !ok || inst.Source != SourceReward {
				t.Errorf("reward %d = %+v, %v, %v", i, inst, ok, err)
			}
		}
		for name, id := range map[string]uuid.UUID{"sold out": soldOut.ID, "archived": archived.ID} {
			if _, ok, err := IssueReward(ctx, tx, mem, id, "staff", uuid.Nil, txnID, e.clk.Now()); ok || err != nil {
				t.Errorf("%s reward: ok %v, %v; want false, nil", name, ok, err)
			}
		}
		if _, _, err := IssueReward(ctx, tx, mem, uuid.New(), "staff", uuid.Nil, txnID, e.clk.Now()); code(err) != "not_found" {
			t.Errorf("unknown template: %v, want not_found", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND txn_id = $2 AND source = 'stamp_reward'", m, txnID); got != 2 {
		t.Errorf("rewards in the operation = %d, want 2", got)
	}
}

// Invariant: a redeem at a store records the store, and the member's list
// names it; a store of another app is never shown.
func TestRedeemRecordsTheStore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.member(t)
	inst := e.issue(t, m, e.template(t, TemplateInput{}).ID)
	store := testutil.NewID()
	e.d.Exec(t, "INSERT INTO stores (id, tenant_id, app_id, name) VALUES ($1, $2, $3, 'Xinyi')", store, e.tenant, e.app)
	st := e.staff()
	st.StoreID = store
	got, err := e.svc.Redeem(ctx, st, inst.ID, idem(), e.scan(t, st, m))
	if err != nil {
		t.Fatal(err)
	}
	if got.Store == nil || got.Store.ID != store || got.Store.Name != "Xinyi" || got.RedeemedAt == nil {
		t.Fatalf("redeemed = %+v", got)
	}
	page, err := e.svc.ListMine(ctx, e.memberP(m), strp("used"), 0, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Store == nil || page.Items[0].Store.Name != "Xinyi" {
		t.Errorf("member list = %+v, %v", page.Items, err)
	}
	var payloadStore string
	if err := e.d.Admin.QueryRow(ctx, "SELECT payload->>'store_id' FROM outbound_events WHERE type = 'coupon.redeemed'").Scan(&payloadStore); err != nil || payloadStore != store.String() {
		t.Errorf("event store_id = %q, %v", payloadStore, err)
	}
}

// Invariant: every clearable template field can be set and then cleared
// back to NULL, an omitted field keeps its value, and the stored row is what
// the update answered; clearing the value of an amount_off template is
// refused because that type needs one.
func TestTemplateFieldsSetAndClear(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tmpl := e.template(t, TemplateInput{})
	from := e.clk.Now().Add(time.Hour)
	to := from.Add(48 * time.Hour)
	set, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{
		Description: strp("  Any drink  "), TotalLimit: intp(10), ValidFrom: &from, ValidTo: &to,
		ValidityDays: intp(3), Claimable: boolp(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Description == nil || *set.Description != "Any drink" || set.TotalLimit == nil || *set.TotalLimit != 10 ||
		set.ValidFrom == nil || !set.ValidFrom.Equal(from) || set.ValidTo == nil || !set.ValidTo.Equal(to) ||
		set.ValidityDays == nil || *set.ValidityDays != 3 || !set.Claimable || set.Value == nil || *set.Value != 50 {
		t.Fatalf("after set: %+v", set)
	}
	for _, field := range []string{"description", "total_limit", "valid_from", "valid_to", "validity_days"} {
		t.Run("clear "+field, func(t *testing.T) {
			if _, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{Clear: []string{field}}); err != nil {
				t.Fatal(err)
			}
		})
	}
	got, err := e.svc.GetTemplate(ctx, e.owner, tmpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != nil || got.TotalLimit != nil || got.ValidFrom != nil || got.ValidTo != nil || got.ValidityDays != nil {
		t.Errorf("after clearing: %+v", got)
	}
	if !got.Claimable || got.Value == nil || *got.Value != 50 || got.Name != tmpl.Name {
		t.Errorf("an omitted field changed: %+v", got)
	}
	if _, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{Clear: []string{"value"}}); code(err) != "validation_failed" {
		t.Errorf("clear the value of amount_off: %v, want validation_failed", err)
	}
	if _, err := e.svc.UpdateTemplate(ctx, e.owner, tmpl.ID, idem(), TemplateUpdate{ValidFrom: &to, ValidTo: &from}); code(err) != "validation_failed" {
		t.Errorf("valid_to before valid_from: %v, want validation_failed", err)
	}
}
