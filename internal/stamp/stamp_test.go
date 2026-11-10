package stamp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/coupon"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// ---- fixtures (real database through TEST_DATABASE_URL)

type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lockedClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *lockedClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type env struct {
	d       *testutil.DB
	clk     *lockedClock
	svc     *Service
	coupons *coupon.Service
	codec   *card.Codec
	tenant  uuid.UUID
	app     uuid.UUID
	owner   principal.Principal
}

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
	tenants := tenant.New(d.App, keyring, clk, log)
	e := &env{d: d, clk: clk, codec: codec}
	e.svc = New(Deps{Pool: d.App, Tenants: tenants, Keyring: keyring, Card: codec, Clock: clk, Log: log, StaffWritesPerMinute: -1})
	e.coupons = coupon.New(coupon.Deps{Pool: d.App, Tenants: tenants, Keyring: keyring, Card: codec, Clock: clk, Log: log,
		StaffWritesPerMinute: -1, ClaimsPerMinute: -1, APIIssuesPerMinute: -1})
	e.tenant = d.NewTenant(t, "Alpha")
	e.app = testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', $3)", e.app, e.tenant, "cafe-"+e.app.String()[24:])
	uid := testutil.NewID()
	d.Exec(t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", uid, uid.String()+"@example.com")
	d.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'owner')", e.tenant, uid)
	e.owner = principal.Principal{Kind: principal.KindUser, UserID: uid}
	return e
}

func (e *env) member(t *testing.T) uuid.UUID {
	t.Helper()
	id := testutil.NewID()
	e.d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", id, e.tenant, e.app, "U"+id.String())
	return id
}

func (e *env) staff() principal.Principal {
	return principal.Principal{Kind: principal.KindStaff, StaffID: testutil.NewID(), StaffRole: "cashier", AppID: e.app, TenantID: e.tenant}
}

func (e *env) scan(t *testing.T, st principal.Principal, memberID uuid.UUID) string {
	t.Helper()
	tok, _, err := e.codec.IssueScanToken(st.StaffID, memberID, st.AppID, st.TenantID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *env) template(t *testing.T, in coupon.TemplateInput) coupon.Template {
	t.Helper()
	if in.Name == "" {
		in.Name, in.Type = "Free coffee", coupon.TypeFreeItem
	}
	tmpl, err := e.coupons.CreateTemplate(context.Background(), e.owner, e.app, idem(), in)
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	return tmpl
}

func (e *env) card(t *testing.T, required, perVisit int, reward *uuid.UUID) Card {
	t.Helper()
	c, err := e.svc.CreateCard(context.Background(), e.owner, e.app, idem(), CardInput{
		Name: "Coffee card", StampsRequired: required, MaxStampsPerVisit: &perVisit, RewardCouponTemplateID: reward,
	})
	if err != nil {
		t.Fatalf("card: %v", err)
	}
	return c
}

func (e *env) stamp(t *testing.T, st principal.Principal, m uuid.UUID, cardID uuid.UUID, n int) Result {
	t.Helper()
	res, err := e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: cardID, Count: n})
	if err != nil {
		t.Fatalf("stamp %d: %v", n, err)
	}
	return res
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

var keyN atomic.Int64

func idem() Idem {
	n := keyN.Add(1)
	return Idem{Key: fmt.Sprintf("k-%d-%s", n, uuid.NewString()), Hash: []byte(fmt.Sprintf("h-%d", n))}
}

func intp(v int) *int { return &v }

func code(err error) string {
	if err == nil {
		return ""
	}
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	return "internal: " + err.Error()
}

// checkLedger asserts the progress row equals the sum of the member's
// stamp events, and completion rows are numbered 1..completions once each.
func (e *env) checkLedger(t *testing.T, m, cardID uuid.UUID, required int) (count, completions int) {
	t.Helper()
	ctx := context.Background()
	var sum, done, distinct, maxAfter int
	if err := e.d.Admin.QueryRow(ctx, `SELECT p.count, p.completions,
		  (SELECT coalesce(sum(delta), 0) FROM stamp_events WHERE member_id = p.member_id AND stamp_card_id = p.stamp_card_id),
		  (SELECT count(DISTINCT completions_after) FROM stamp_events WHERE member_id = p.member_id AND stamp_card_id = p.stamp_card_id AND reason = 'completion'),
		  (SELECT coalesce(max(completions_after), 0) FROM stamp_events WHERE member_id = p.member_id AND stamp_card_id = p.stamp_card_id AND reason = 'completion')
		FROM stamp_progress p WHERE p.member_id = $1 AND p.stamp_card_id = $2`, m, cardID).Scan(&count, &completions, &sum, &distinct, &maxAfter); err != nil {
		t.Fatal(err)
	}
	if sum != count {
		t.Errorf("progress count %d, events sum %d", count, sum)
	}
	if count < 0 || count >= required {
		t.Errorf("count %d outside 0..%d", count, required-1)
	}
	if done = e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1 AND stamp_card_id = $2 AND reason = 'completion'", m, cardID); done != completions || distinct != completions || maxAfter != completions {
		t.Errorf("completions %d, completion rows %d, distinct numbers %d, highest %d", completions, done, distinct, maxAfter)
	}
	return count, completions
}

// ---- behaviour

// Invariant: concurrent stamps on one member's card serialize on the
// progress row. No completion is skipped or doubled, each completion
// issues exactly one reward coupon and one stamp.completed event, and the
// count equals the sum of the events.
func TestParallelStampsNeverSkipOrDoubleACompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	cases := []struct {
		name     string
		required int
		perVisit int
		workers  int
		each     int
	}{
		// The cap is the card's maximum so the whole race fits in one
		// visit window; TestVisitCap* cover the cap itself.
		{"one stamp each", 5, 100, 42, 1},
		{"two stamps each", 3, 100, 30, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			reward := e.template(t, coupon.TemplateInput{})
			c := e.card(t, tc.required, tc.perVisit, &reward.ID)
			m := e.member(t)
			st := e.staff()
			tokens := make([]string, tc.workers)
			for i := range tokens {
				tokens[i] = e.scan(t, st, m)
			}
			var completed, rewards atomic.Int64
			errs := make([]error, tc.workers)
			release := make(chan struct{})
			var wg sync.WaitGroup
			for i := range tc.workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-release
					res, err := e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: tokens[i], StampCardID: c.ID, Count: tc.each})
					errs[i] = err
					completed.Add(int64(res.Completed))
					rewards.Add(int64(len(res.Rewards)))
				}()
			}
			close(release)
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("stamp %d: %v", i, err)
				}
			}
			total := tc.workers * tc.each
			count, completions := e.checkLedger(t, m, c.ID, tc.required)
			if count != total%tc.required || completions != total/tc.required {
				t.Errorf("count %d completions %d, want %d and %d", count, completions, total%tc.required, total/tc.required)
			}
			if int(completed.Load()) != completions || int(rewards.Load()) != completions {
				t.Errorf("results report %d completions and %d rewards, want %d", completed.Load(), rewards.Load(), completions)
			}
			if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND source = 'stamp_reward'", m); got != completions {
				t.Errorf("reward coupons = %d, want %d", got, completions)
			}
			if got := e.count(t, "SELECT count(*) FROM outbound_events WHERE type = 'stamp.completed' AND member_id = $1", m); got != completions {
				t.Errorf("stamp.completed events = %d, want %d", got, completions)
			}
		})
	}
}

// Invariant: all stamps given on one scan to one card together stay within
// max_stamps_per_visit, even when the requests race. A new scan inside the
// visit window is the same visit; after VisitWindow a new visit starts.
func TestParallelStampsOnOneScanRespectTheVisitCap(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	c := e.card(t, 10, 3, nil)
	m := e.member(t)
	st := e.staff()
	tok := e.scan(t, st, m)
	const workers = 12
	codes := make([]string, workers)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			_, err := e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: tok, StampCardID: c.ID, Count: 1})
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
		case "stamp_visit_limit":
		default:
			t.Fatalf("stamp %d: %s", i, c)
		}
	}
	if ok != 3 {
		t.Errorf("%d stamps on one scan succeeded, want 3", ok)
	}
	if count, _ := e.checkLedger(t, m, c.ID, 10); count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	// A new scan inside the window is still the same visit.
	if _, err := e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: c.ID, Count: 1}); code(err) != "stamp_visit_limit" {
		t.Errorf("new scan inside the visit window: %v, want stamp_visit_limit", err)
	}
	e.clk.Advance(VisitWindow)
	if res := e.stamp(t, st, m, c.ID, 3); res.Progress.Count != 6 {
		t.Errorf("after a second visit: %+v", res.Progress)
	}
	e.clk.Advance(VisitWindow)
	// One request above the cap is refused whole.
	if _, err := e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: c.ID, Count: 4}); code(err) != "stamp_visit_limit" {
		t.Errorf("4 stamps on a 3-per-visit card: %v", err)
	}
}

// Invariant (product decision 2026-11-10): when the reward template is sold
// out, archived or outside its window, the stamps and the completion are
// still recorded, no coupon is issued, the completion row carries the note
// reward_unavailable, and the result and the progress count it.
func TestUnavailableRewardStillCompletes(t *testing.T) {
	cases := []struct {
		name string
		make func(e *env, t *testing.T) uuid.UUID
	}{
		{"sold out", func(e *env, t *testing.T) uuid.UUID {
			tmpl := e.template(t, coupon.TemplateInput{TotalLimit: intp(1)})
			if _, err := e.coupons.Issue(context.Background(), e.owner, e.member(t), tmpl.ID, idem()); err != nil {
				t.Fatal(err)
			}
			return tmpl.ID
		}},
		{"archived", func(e *env, t *testing.T) uuid.UUID {
			tmpl := e.template(t, coupon.TemplateInput{})
			archived := "archived"
			if _, err := e.coupons.UpdateTemplate(context.Background(), e.owner, tmpl.ID, idem(), coupon.TemplateUpdate{Status: &archived}); err != nil {
				t.Fatal(err)
			}
			return tmpl.ID
		}},
		{"window over", func(e *env, t *testing.T) uuid.UUID {
			end := e.clk.Now().Add(time.Hour)
			tmpl := e.template(t, coupon.TemplateInput{Name: "Free tea", Type: coupon.TypeFreeItem, ValidTo: &end})
			e.clk.Advance(2 * time.Hour)
			return tmpl.ID
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			reward := tc.make(e, t)
			c := e.card(t, 2, 3, &reward)
			m := e.member(t)
			st := e.staff()
			res := e.stamp(t, st, m, c.ID, 3)
			if res.Completed != 1 || res.RewardsUnavailable != 1 || len(res.Rewards) != 0 || res.Progress.Count != 1 {
				t.Fatalf("result = %+v", res)
			}
			if res.Progress.UnrewardedCompletions != 1 || res.Progress.Completions != 1 {
				t.Errorf("progress = %+v", res.Progress)
			}
			var note *string
			var couponID uuid.NullUUID
			if err := e.d.Admin.QueryRow(context.Background(), "SELECT note, coupon_instance_id FROM stamp_events WHERE member_id = $1 AND reason = 'completion'", m).Scan(&note, &couponID); err != nil {
				t.Fatal(err)
			}
			if note == nil || *note != NoteRewardUnavailable || couponID.Valid {
				t.Errorf("completion row note %v coupon %v", note, couponID)
			}
			if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1", m); got != 0 {
				t.Errorf("member got %d coupons", got)
			}
			var unavailable bool
			if err := e.d.Admin.QueryRow(context.Background(), "SELECT (payload->>'reward_unavailable')::boolean FROM outbound_events WHERE type = 'stamp.completed' AND member_id = $1", m).Scan(&unavailable); err != nil || !unavailable {
				t.Errorf("stamp.completed reward_unavailable = %v, %v", unavailable, err)
			}
		})
	}

	t.Run("a card without a reward just completes", func(t *testing.T) {
		e := newEnv(t)
		c := e.card(t, 2, 2, nil)
		m := e.member(t)
		res := e.stamp(t, e.staff(), m, c.ID, 2)
		if res.Completed != 1 || res.RewardsUnavailable != 0 || res.Progress.UnrewardedCompletions != 0 || res.Progress.Count != 0 {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("an available reward is issued with source stamp_reward", func(t *testing.T) {
		e := newEnv(t)
		tmpl := e.template(t, coupon.TemplateInput{})
		c := e.card(t, 2, 2, &tmpl.ID)
		m := e.member(t)
		res := e.stamp(t, e.staff(), m, c.ID, 2)
		if res.Completed != 1 || len(res.Rewards) != 1 || res.Rewards[0].Source != coupon.SourceReward || res.Rewards[0].MemberID != m {
			t.Fatalf("result = %+v", res)
		}
		if res.Progress.Reward == nil || res.Progress.Reward.ID != tmpl.ID {
			t.Errorf("progress reward = %+v", res.Progress.Reward)
		}
	})
}

// Invariant: dashboard adjustments are new event rows; they cannot take
// the count below zero, a positive one completes the card, and archived
// cards can be corrected.
func TestAdjustments(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tmpl := e.template(t, coupon.TemplateInput{})
	c := e.card(t, 4, 1, &tmpl.ID)
	m := e.member(t)

	steps := []struct {
		name      string
		delta     int
		code      string
		count     int
		completed int
	}{
		{"add three", 3, "", 3, 0},
		{"below zero", -4, "insufficient_stamps", 3, 0},
		{"remove two", -2, "", 1, 0},
		{"complete twice", 7, "", 0, 2},
		{"zero", 0, "validation_failed", 0, 0},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			res, err := e.svc.Adjust(ctx, e.owner, m, idem(), c.ID, s.delta, "correction")
			if code(err) != s.code {
				t.Fatalf("adjust %d: %v, want %q", s.delta, err, s.code)
			}
			if err == nil && (res.Progress.Count != s.count || res.Completed != s.completed || len(res.Rewards) != s.completed) {
				t.Fatalf("result = %+v", res)
			}
		})
	}
	e.checkLedger(t, m, c.ID, 4)
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1 AND reason = 'adjust' AND scan_id IS NULL", m); got != 3 {
		t.Errorf("adjust rows = %d, want 3", got)
	}
	if got := e.count(t, "SELECT count(*) FROM audit_log WHERE action = 'stamp.adjust'"); got != 3 {
		t.Errorf("audit rows = %d, want 3", got)
	}

	archived := "archived"
	if _, err := e.svc.UpdateCard(ctx, e.owner, c.ID, idem(), CardUpdate{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Adjust(ctx, e.owner, m, idem(), c.ID, 1, "on an archived card"); err != nil {
		t.Errorf("adjust an archived card: %v", err)
	}
	st := e.staff()
	if _, err := e.svc.StaffStamp(ctx, st, idem(), StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: c.ID, Count: 1}); code(err) != "stamp_card_inactive" {
		t.Errorf("staff stamp on an archived card: %v", err)
	}
	mine, err := e.svc.ListMember(ctx, e.owner, m)
	if err != nil || len(mine) != 1 || mine[0].Status != "archived" {
		t.Errorf("dashboard progress with an archived card = %+v, %v", mine, err)
	}
	own, err := e.svc.ListMine(ctx, principal.Principal{Kind: principal.KindMember, MemberID: m, AppID: e.app, TenantID: e.tenant})
	if err != nil || len(own) != 0 {
		t.Errorf("member progress lists archived cards: %+v, %v", own, err)
	}
}

// Invariant: stamps_required is fixed once any member has a stamp, and a
// reward template must belong to the card's app.
func TestCardRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.card(t, 5, 1, nil)
	if _, err := e.svc.UpdateCard(ctx, e.owner, c.ID, idem(), CardUpdate{StampsRequired: intp(6)}); err != nil {
		t.Fatalf("change stamps_required before use: %v", err)
	}
	e.stamp(t, e.staff(), e.member(t), c.ID, 1)
	cases := []struct {
		name string
		in   CardUpdate
		code string
	}{
		{"stamps_required in use", CardUpdate{StampsRequired: intp(7)}, "stamp_card_in_use"},
		{"same stamps_required", CardUpdate{StampsRequired: intp(6)}, ""},
		{"rename", CardUpdate{Name: strp("Latte card")}, ""},
		{"too few stamps", CardUpdate{StampsRequired: intp(1)}, "validation_failed"},
		{"reward of another app", CardUpdate{RewardCouponTemplateID: ptrUUID(uuid.New())}, "not_found"},
		{"send and clear reward", CardUpdate{RewardCouponTemplateID: ptrUUID(uuid.New()), ClearReward: true}, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.svc.UpdateCard(ctx, e.owner, c.ID, idem(), tc.in); code(err) != tc.code {
				t.Fatalf("update: %v, want %q", err, tc.code)
			}
		})
	}
	tmpl := e.template(t, coupon.TemplateInput{})
	got, err := e.svc.UpdateCard(ctx, e.owner, c.ID, idem(), CardUpdate{RewardCouponTemplateID: &tmpl.ID})
	if err != nil || got.RewardCouponTemplateID == nil || *got.RewardCouponTemplateID != tmpl.ID {
		t.Fatalf("set reward: %+v, %v", got, err)
	}
	got, err = e.svc.UpdateCard(ctx, e.owner, c.ID, idem(), CardUpdate{ClearReward: true})
	if err != nil || got.RewardCouponTemplateID != nil {
		t.Fatalf("clear reward: %+v, %v", got, err)
	}
	if _, err := e.svc.CreateCard(ctx, e.owner, e.app, idem(), CardInput{Name: "x", StampsRequired: 5, RewardCouponTemplateID: ptrUUID(uuid.New())}); code(err) != "not_found" {
		t.Errorf("create with a foreign reward: %v", err)
	}
}

func strp(v string) *string          { return &v }
func ptrUUID(v uuid.UUID) *uuid.UUID { return &v }

// Invariant: a stamp runs once per Idempotency-Key; a replay returns the
// stored result even after the scan expired, a different payload is 422,
// and a new write with the expired scan is refused.
func TestStampIdempotency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.card(t, 5, 2, nil)
	m := e.member(t)
	st := e.staff()
	tok := e.scan(t, st, m)
	k := idem()
	in := StaffStampInput{ScanToken: tok, StampCardID: c.ID, Count: 1}
	first, err := e.svc.StaffStamp(ctx, st, k, in)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(10 * time.Minute)
	replay, err := e.svc.StaffStamp(ctx, st, k, in)
	if err != nil || replay.Progress.Count != first.Progress.Count || replay.Added != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := e.svc.StaffStamp(ctx, st, Idem{Key: k.Key, Hash: []byte("other")}, in); code(err) != "idempotency_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := e.svc.StaffStamp(ctx, st, idem(), in); code(err) != "scan_invalid" {
		t.Fatalf("expired scan: %v", err)
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1", m); got != 1 {
		t.Errorf("events = %d, want 1", got)
	}
	// Binding: another staff member cannot use the scan.
	if _, err := e.svc.StaffStamp(ctx, e.staff(), idem(), StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: c.ID, Count: 1}); code(err) != "scan_mismatch" {
		t.Errorf("another staff member's scan: %v", err)
	}
}

// Invariant: outbound events, reward coupons and stamp events of one
// operation commit together: a transaction that fails after a completion
// leaves none of them.
func TestStampRollsBackWithTheEvents(t *testing.T) {
	e := newEnv(t)
	tmpl := e.template(t, coupon.TemplateInput{})
	cd := e.card(t, 2, 2, &tmpl.ID)
	m := e.member(t)
	errLater := errors.New("a later step failed")
	err := db.WithTenant(context.Background(), e.d.App, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		mem, err := access.LoadMember(ctx, q, m, e.app)
		if err != nil {
			return err
		}
		c, err := cardForShare(ctx, q, e.app, cd.ID)
		if err != nil {
			return err
		}
		scan := "scan-1"
		res, err := e.svc.apply(ctx, tx, mem, c, operation{reason: ReasonStamp, delta: 2, scanID: &scan, actorType: "system"}, e.clk.Now())
		if err != nil {
			return err
		}
		if res.Completed != 1 || len(res.Rewards) != 1 {
			t.Errorf("inside the transaction: %+v", res)
		}
		return errLater
	})
	if !errors.Is(err, errLater) {
		t.Fatalf("transaction: %v", err)
	}
	for _, q := range []string{
		"SELECT count(*) FROM outbound_events",
		"SELECT count(*) FROM stamp_events",
		"SELECT count(*) FROM coupon_instances",
		"SELECT count(*) FROM stamp_progress",
	} {
		if got := e.count(t, q); got != 0 {
			t.Errorf("%s = %d after the rollback", q, got)
		}
	}
}

// Invariant: stamp cards and progress of one tenant are invisible to
// another tenant's users.
func TestCrossTenantStamps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.card(t, 5, 1, nil)
	m := e.member(t)
	e.stamp(t, e.staff(), m, c.ID, 1)
	tenantB := e.d.NewTenant(t, "Beta")
	uid := testutil.NewID()
	e.d.Exec(t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", uid, uid.String()+"@example.com")
	e.d.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'owner')", tenantB, uid)
	ownerB := principal.Principal{Kind: principal.KindUser, UserID: uid}
	checks := map[string]func() error{
		"get card": func() error { _, err := e.svc.GetCard(ctx, ownerB, c.ID); return err },
		"update card": func() error {
			_, err := e.svc.UpdateCard(ctx, ownerB, c.ID, idem(), CardUpdate{Name: strp("x")})
			return err
		},
		"list cards":    func() error { _, err := e.svc.ListCards(ctx, ownerB, e.app, nil); return err },
		"list progress": func() error { _, err := e.svc.ListMember(ctx, ownerB, m); return err },
		"adjust":        func() error { _, err := e.svc.Adjust(ctx, ownerB, m, idem(), c.ID, 1, "x"); return err },
	}
	for name, call := range checks {
		t.Run(name, func(t *testing.T) {
			if err := call(); code(err) != "not_found" {
				t.Fatalf("%v, want not_found", err)
			}
		})
	}
	// A staff member of another app cannot stamp app A's card with a scan
	// of app A's member that names its own app.
	appB := testutil.NewID()
	e.d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Bistro', $3)", appB, tenantB, "bistro-"+appB.String()[24:])
	stB := principal.Principal{Kind: principal.KindStaff, StaffID: testutil.NewID(), AppID: appB, TenantID: tenantB}
	if _, err := e.svc.StaffStamp(ctx, stB, idem(), StaffStampInput{ScanToken: e.scan(t, stB, m), StampCardID: c.ID, Count: 1}); code(err) != "not_found" {
		t.Errorf("staff of app B: %v, want not_found", err)
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events"); got != 1 {
		t.Errorf("stamp events = %d, want 1", got)
	}
}

// Invariant (product decision 2026-11-10): max_stamps_per_visit counts the
// member's stamps on the card in the last VisitWindow, whatever the scan.
// Scanning twice does not open a second visit; once the window has passed,
// stamping works again.
func TestVisitCapCountsStampsAcrossScans(t *testing.T) {
	e := newEnv(t)
	c := e.card(t, 10, 1, nil)
	m := e.member(t)
	st, other := e.staff(), e.staff()
	e.stamp(t, st, m, c.ID, 1)

	// A second scan, by the same or another cashier, a minute later.
	e.clk.Advance(time.Minute)
	for _, s := range []principal.Principal{st, other} {
		_, err := e.svc.StaffStamp(context.Background(), s, idem(), StaffStampInput{ScanToken: e.scan(t, s, m), StampCardID: c.ID, Count: 1})
		if code(err) != "stamp_visit_limit" {
			t.Fatalf("second scan inside the window: %v, want stamp_visit_limit", err)
		}
		var ae *apierr.Error
		if !errors.As(err, &ae) || ae.Status != 409 {
			t.Fatalf("status: %v", err)
		}
	}
	// Another member and another card are their own visits.
	e.stamp(t, st, e.member(t), c.ID, 1)
	e.stamp(t, st, m, e.card(t, 10, 1, nil).ID, 1)

	// 29 minutes after the stamp the window still holds it.
	e.clk.Advance(28 * time.Minute)
	if _, err := e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: c.ID, Count: 1}); code(err) != "stamp_visit_limit" {
		t.Fatalf("29 minutes later: %v, want stamp_visit_limit", err)
	}
	// 30 minutes after the stamp it has left the window.
	e.clk.Advance(time.Minute)
	if res := e.stamp(t, st, m, c.ID, 1); res.Progress.Count != 2 {
		t.Errorf("after the window: %+v", res.Progress)
	}
	// The stamp rows are dated by the service clock and keep their scans.
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1 AND stamp_card_id = $2 AND reason = 'stamp' AND scan_id IS NOT NULL AND created_at IN ($3, $4)",
		m, c.ID, time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)); got != 2 {
		t.Errorf("stamp rows at the clock's times = %d, want 2", got)
	}
}

// Invariant: stamps racing on two different scans of one member and card
// share one visit: together they never pass max_stamps_per_visit.
func TestVisitCapHoldsAcrossConcurrentScans(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	c := e.card(t, 50, 2, nil)
	m := e.member(t)
	st, other := e.staff(), e.staff()
	tokens := []string{e.scan(t, st, m), e.scan(t, other, m)}
	staff := []principal.Principal{st, other}
	const workers = 20
	codes := make([]string, workers)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			_, err := e.svc.StaffStamp(context.Background(), staff[i%2], idem(), StaffStampInput{ScanToken: tokens[i%2], StampCardID: c.ID, Count: 1})
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
		case "stamp_visit_limit":
		default:
			t.Fatalf("stamp %d: %s", i, c)
		}
	}
	if ok != 2 {
		t.Errorf("%d stamps across two scans succeeded, want 2", ok)
	}
	if count, _ := e.checkLedger(t, m, c.ID, 50); count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// Staff stamps are audited with the staff member, store, card, count and
// completions; the adjustment audit carries no note, since audit rows
// outlive a PDPA erasure.
func TestStaffStampsAreAudited(t *testing.T) {
	e := newEnv(t)
	c := e.card(t, 2, 3, nil)
	m := e.member(t)
	st := e.staff()
	st.StoreID = testutil.NewID()
	e.stamp(t, st, m, c.ID, 3)
	var actorType, meta string
	if err := e.d.Admin.QueryRow(context.Background(), "SELECT actor_type, metadata::text FROM audit_log WHERE action = 'stamp.add' AND target_id = $1", m.String()).Scan(&actorType, &meta); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"staff_id": "` + st.StaffID.String(), `"store_id": "` + st.StoreID.String(), `"stamp_card_id": "` + c.ID.String(), `"count": 3`, `"completed": 1`} {
		if !strings.Contains(meta, want) {
			t.Errorf("stamp.add metadata %s lacks %s", meta, want)
		}
	}
	if actorType != "staff" {
		t.Errorf("actor_type = %s", actorType)
	}
	if _, err := e.svc.Adjust(context.Background(), e.owner, m, idem(), c.ID, 1, "phoned 0912-345-678"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Admin.QueryRow(context.Background(), "SELECT metadata::text FROM audit_log WHERE action = 'stamp.adjust' AND target_id = $1", m.String()).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta, "0912") || strings.Contains(meta, "note") {
		t.Errorf("stamp.adjust audit metadata holds the note: %s", meta)
	}
}
