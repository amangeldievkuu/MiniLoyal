package stamp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/coupon"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Test-engineer additions for Phase 4: 200-goroutine races that mix staff
// stamps, dashboard adjustments and coupon claims; idempotency under
// concurrency and after a change of authorization; scan negatives.

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

// Invariant: 200 concurrent staff stamps and dashboard adjustments (up and
// down) on one member's card never take the count below zero; the
// progress row equals the sum of the events; every completion is recorded
// once and either issued a reward or is marked reward_unavailable; and the
// reward template never passes its total_limit.
func TestTwoHundredMixedStampWritesKeepTheLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	const n, required, rewardLimit = 200, 4, 5
	reward := e.template(t, coupon.TemplateInput{TotalLimit: intp(rewardLimit)})
	c := e.card(t, required, 1, &reward.ID)
	m := e.member(t)
	st := e.staff()
	tokens := make([]string, n)
	for i := range tokens {
		tokens[i] = e.scan(t, st, m)
	}
	var mu sync.Mutex
	codes := map[string]int{}
	var completed, rewarded, unavailable int
	run(n, func(i int) {
		var res Result
		var err error
		switch i % 4 {
		case 0, 1:
			res, err = e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: tokens[i], StampCardID: c.ID, Count: 1})
		case 2:
			res, err = e.svc.Adjust(context.Background(), e.owner, m, idem(), c.ID, 2, "bonus")
		default:
			res, err = e.svc.Adjust(context.Background(), e.owner, m, idem(), c.ID, -1, "correction")
		}
		mu.Lock()
		defer mu.Unlock()
		codes[code(err)]++
		completed += res.Completed
		rewarded += len(res.Rewards)
		unavailable += res.RewardsUnavailable
	})
	for c := range codes {
		if c != "" && c != "insufficient_stamps" {
			t.Fatalf("unexpected outcome %q (%d times); all: %v", c, codes[c], codes)
		}
	}
	_, completions := e.checkLedger(t, m, c.ID, required)
	if completed != completions || rewarded+unavailable != completions {
		t.Errorf("results: %d completed, %d rewarded, %d unavailable; ledger %d completions", completed, rewarded, unavailable, completions)
	}
	if want := min(completions, rewardLimit); rewarded != want {
		t.Errorf("rewards issued %d, want %d", rewarded, want)
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1 AND count_after < 0", m); got != 0 {
		t.Errorf("%d events left a negative count", got)
	}
	if got := e.count(t, "SELECT claimed_count FROM coupon_templates WHERE id = $1", reward.ID); got > rewardLimit || got != rewarded {
		t.Errorf("reward claimed_count = %d, rewards %d, limit %d", got, rewarded, rewardLimit)
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1 AND reason = 'completion' AND note = 'reward_unavailable'", m); got != unavailable {
		t.Errorf("reward_unavailable rows = %d, want %d", got, unavailable)
	}
	if got := e.count(t, "SELECT count(*) FROM outbound_events WHERE type = 'stamp.completed' AND member_id = $1", m); got != completions {
		t.Errorf("stamp.completed events = %d, want %d", got, completions)
	}
}

// Invariant: when many members complete cards that share one reward
// template while other members claim it, the template never issues more
// than total_limit coupons in all, and every completion that lost the race
// for the last coupon is still recorded as reward_unavailable.
func TestRewardsAndClaimsShareTotalLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	const n, limit = 200, 30
	reward := e.template(t, coupon.TemplateInput{Name: "Free cake", Type: coupon.TypeFreeItem, TotalLimit: intp(limit), Claimable: boolp(true)})
	c := e.card(t, 2, 2, &reward.ID)
	members := make([]uuid.UUID, n)
	tokens := make([]string, n)
	st := e.staff()
	for i := range members {
		members[i] = e.member(t)
		tokens[i] = e.scan(t, st, members[i])
	}
	var mu sync.Mutex
	var rewards, unavailable, claims int
	run(n, func(i int) {
		if i%2 == 0 {
			res, err := e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: tokens[i], StampCardID: c.ID, Count: 2})
			if err != nil {
				t.Errorf("stamp %d: %v", i, err)
				return
			}
			mu.Lock()
			rewards += len(res.Rewards)
			unavailable += res.RewardsUnavailable
			mu.Unlock()
			return
		}
		mp := principal.Principal{Kind: principal.KindMember, MemberID: members[i], AppID: e.app, TenantID: e.tenant}
		_, err := e.coupons.Claim(context.Background(), mp, reward.ID, idem())
		switch code(err) {
		case "":
			mu.Lock()
			claims++
			mu.Unlock()
		case "coupon_sold_out":
		default:
			t.Errorf("claim %d: %v", i, err)
		}
	})
	if rewards+claims != limit {
		t.Errorf("%d rewards + %d claims, want total_limit %d", rewards, claims, limit)
	}
	if rewards+unavailable != n/2 {
		t.Errorf("%d rewards + %d unavailable, want %d completions", rewards, unavailable, n/2)
	}
	if got := e.count(t, "SELECT count(*) FROM coupon_instances WHERE template_id = $1", reward.ID); got != limit {
		t.Errorf("instances = %d, want %d", got, limit)
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE reason = 'completion' AND note = 'reward_unavailable'"); got != unavailable {
		t.Errorf("reward_unavailable rows = %d, want %d", got, unavailable)
	}
}

// Invariant: stamps_required cannot change once a member has a stamp. A
// first stamp racing an update of stamps_required either lands after the
// update (and counts against the new value) or makes the update fail with
// stamp_card_in_use; it never counts against a value that changes under it.
func TestFirstStampRacingStampsRequiredChange(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	const cards = 40
	st := e.staff()
	type race struct {
		card     Card
		member   uuid.UUID
		token    string
		stamp    Result
		stampErr error
		upErr    error
	}
	races := make([]*race, cards)
	for i := range races {
		r := &race{card: e.card(t, 5, 1, nil), member: e.member(t)}
		r.token = e.scan(t, st, r.member)
		races[i] = r
	}
	run(2*cards, func(i int) {
		r := races[i/2]
		if i%2 == 0 {
			r.stamp, r.stampErr = e.svc.StaffStamp(context.Background(), st, idem(), StaffStampInput{ScanToken: r.token, StampCardID: r.card.ID, Count: 1})
		} else {
			_, r.upErr = e.svc.UpdateCard(context.Background(), e.owner, r.card.ID, idem(), CardUpdate{StampsRequired: intp(7)})
		}
	})
	for i, r := range races {
		if r.stampErr != nil {
			t.Fatalf("card %d stamp: %v", i, r.stampErr)
		}
		switch code(r.upErr) {
		case "":
			if r.stamp.Progress.StampsRequired != 7 {
				t.Errorf("card %d: update succeeded but the stamp counted against %d", i, r.stamp.Progress.StampsRequired)
			}
		case "stamp_card_in_use":
			if r.stamp.Progress.StampsRequired != 5 {
				t.Errorf("card %d: update refused but the stamp saw %d", i, r.stamp.Progress.StampsRequired)
			}
		default:
			t.Fatalf("card %d update: %v", i, r.upErr)
		}
	}
}

// Invariant: 200 concurrent stamps with one Idempotency-Key and payload
// write one stamp event; every answer is the same result.
func TestSameKeyConcurrentStampsRunOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	e := newEnv(t)
	c := e.card(t, 5, 3, nil)
	m := e.member(t)
	st := e.staff()
	in := StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: c.ID, Count: 1}
	k := idem()
	const n = 200
	got := make([][]byte, n)
	codes := make([]string, n)
	run(n, func(i int) {
		res, err := e.svc.StaffStamp(context.Background(), st, k, in)
		codes[i] = code(err)
		got[i], _ = json.Marshal(res)
	})
	var first []byte
	for i := range n {
		switch codes[i] {
		case "":
			if first == nil {
				first = got[i]
			} else if !bytes.Equal(first, got[i]) {
				t.Fatalf("answer %d differs:\n%s\n%s", i, got[i], first)
			}
		case "conflict":
		default:
			t.Fatalf("stamp %d: %s", i, codes[i])
		}
	}
	if first == nil {
		t.Fatal("no stamp succeeded")
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1", m); got != 1 {
		t.Errorf("stamp events = %d, want 1", got)
	}
}

// Invariant: authorization runs before a replay: an adjustment's key no
// longer replays once its author is demoted below admin or removed, and an
// adjustment with the same key and another payload is 422.
func TestAdjustReplayIsReauthorized(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.card(t, 5, 1, nil)
	m := e.member(t)
	uid := testutil.NewID()
	e.d.Exec(t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", uid, uid.String()+"@example.com")
	e.d.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", e.tenant, uid)
	admin := principal.Principal{Kind: principal.KindUser, UserID: uid}

	k := idem()
	first, err := e.svc.Adjust(ctx, admin, m, k, c.ID, 2, "welcome")
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.Adjust(ctx, admin, m, k, c.ID, 2, "welcome")
	if err != nil || again.Progress.Count != first.Progress.Count {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := e.svc.Adjust(ctx, admin, m, Idem{Key: k.Key, Hash: []byte("other")}, c.ID, 3, "welcome"); code(err) != "idempotency_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	e.d.Exec(t, "UPDATE tenant_users SET role = 'staff' WHERE user_id = $1", uid)
	if _, err := e.svc.Adjust(ctx, admin, m, k, c.ID, 2, "welcome"); err == nil {
		t.Error("a demoted user replayed an adjustment")
	}
	e.d.Exec(t, "DELETE FROM tenant_users WHERE user_id = $1", uid)
	if _, err := e.svc.Adjust(ctx, admin, m, k, c.ID, 2, "welcome"); code(err) != "not_found" {
		t.Errorf("removed user's replay: %v, want not_found", err)
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1", m); got != 1 {
		t.Errorf("stamp events = %d, want 1", got)
	}
}

// Invariant: a tampered, foreign-key, rotated or expired scan never adds a
// stamp, and API keys and members cannot use the dashboard stamp
// operations.
func TestStampRefusesBadScansAndOtherPrincipals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.card(t, 5, 3, nil)
	m := e.member(t)
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
	tampered := []byte(good)
	tampered[len(tampered)/2] ^= 0x01
	cases := []struct{ name, token string }{
		{"empty", ""},
		{"tampered", string(tampered)},
		{"another signing key", foreign},
		{"old card key version", rotated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.svc.StaffStamp(ctx, st, idem(), StaffStampInput{ScanToken: tc.token, StampCardID: c.ID, Count: 1}); code(err) != "scan_invalid" {
				t.Fatalf("stamp: %v, want scan_invalid", err)
			}
		})
	}
	e.clk.Advance(10 * time.Minute)
	if _, err := e.svc.StaffStamp(ctx, st, idem(), StaffStampInput{ScanToken: good, StampCardID: c.ID, Count: 1}); code(err) != "scan_invalid" {
		t.Errorf("expired scan: %v, want scan_invalid", err)
	}

	key := principal.Principal{Kind: principal.KindAPIKey, APIKeyID: testutil.NewID(), TenantID: e.tenant, AppID: e.app}
	mp := principal.Principal{Kind: principal.KindMember, MemberID: m, AppID: e.app, TenantID: e.tenant}
	others := map[string]func() error{
		"key adjusts":        func() error { _, err := e.svc.Adjust(ctx, key, m, idem(), c.ID, 1, "x"); return err },
		"member adjusts":     func() error { _, err := e.svc.Adjust(ctx, mp, m, idem(), c.ID, 1, "x"); return err },
		"key lists progress": func() error { _, err := e.svc.ListMember(ctx, key, m); return err },
		"key updates card": func() error {
			_, err := e.svc.UpdateCard(ctx, key, c.ID, idem(), CardUpdate{Name: strp("x")})
			return err
		},
		"member stamps": func() error {
			_, err := e.svc.StaffStamp(ctx, mp, idem(), StaffStampInput{ScanToken: good, StampCardID: c.ID, Count: 1})
			return err
		},
	}
	for name, call := range others {
		t.Run(name, func(t *testing.T) {
			err := call()
			ae, ok := apierr.As(err)
			if !ok || (ae.Status != 401 && ae.Status != 403 && ae.Status != 404) {
				t.Fatalf("%v, want 401, 403 or 404", err)
			}
		})
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events"); got != 0 {
		t.Errorf("stamp events = %d, want 0", got)
	}
}

func boolp(v bool) *bool { return &v }

// Invariant: every team member reads cards; the status filter narrows the
// list; a member sees only active cards, their own progress and nothing of
// another member; a data-rights export lists every stamp event of the
// member, completions included, and only theirs.
func TestCardReadsAndExport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	uid := testutil.NewID()
	e.d.Exec(t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", uid, uid.String()+"@example.com")
	e.d.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'staff')", e.tenant, uid)
	staffU := principal.Principal{Kind: principal.KindUser, UserID: uid}

	reward := e.template(t, coupon.TemplateInput{})
	active := e.card(t, 2, 2, &reward.ID)
	old := e.card(t, 3, 1, nil)
	m, other := e.member(t), e.member(t)
	st := e.staff()
	e.stamp(t, st, m, active.ID, 2) // completes once
	e.stamp(t, st, m, old.ID, 1)
	e.stamp(t, st, other, active.ID, 1)
	if _, err := e.svc.UpdateCard(ctx, e.owner, old.ID, idem(), CardUpdate{Status: strp("archived")}); err != nil {
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
		{strp("paused"), 0, "bad_request"},
	}
	for _, tc := range cases {
		got, err := e.svc.ListCards(ctx, staffU, e.app, tc.status)
		if code(err) != tc.code || len(got) != tc.want {
			t.Errorf("list %v: %d cards, %v; want %d, %q", tc.status, len(got), err, tc.want, tc.code)
		}
	}
	if got, err := e.svc.GetCard(ctx, staffU, active.ID); err != nil || got.ID != active.ID {
		t.Errorf("staff get = %+v, %v", got, err)
	}
	if _, err := e.svc.GetCard(ctx, staffU, uuid.New()); code(err) != "not_found" {
		t.Errorf("unknown card: %v", err)
	}
	if _, err := e.svc.ListCards(ctx, staffU, uuid.New(), nil); code(err) != "not_found" {
		t.Errorf("unknown app: %v", err)
	}
	if _, err := e.svc.CreateCard(ctx, staffU, e.app, idem(), CardInput{Name: "x", StampsRequired: 5}); code(err) != "forbidden" && code(err) != "not_found" {
		t.Errorf("staff creates a card: %v", err)
	}

	mine, err := e.svc.ListMine(ctx, principal.Principal{Kind: principal.KindMember, MemberID: m, AppID: e.app, TenantID: e.tenant})
	if err != nil || len(mine) != 1 || mine[0].StampCardID != active.ID || mine[0].Completions != 1 || mine[0].Count != 0 {
		t.Errorf("member progress = %+v, %v", mine, err)
	}
	if _, err := e.svc.ListMine(ctx, e.owner); code(err) != "not_found" {
		t.Errorf("a user lists member progress as a member: %v", err)
	}

	err = db.WithTenant(ctx, e.d.App, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		mem, err := access.LoadMember(ctx, q, m, e.app)
		if err != nil {
			return err
		}
		data, err := ExportMember(ctx, q, mem)
		if err != nil {
			return err
		}
		if len(data.Progress) != 2 {
			t.Errorf("export progress = %d cards, want 2 (archived included)", len(data.Progress))
		}
		reasons := map[string]int{}
		for _, ev := range data.Events {
			reasons[ev.Reason]++
		}
		if len(data.Events) != 3 || reasons[ReasonStamp] != 2 || reasons[ReasonCompletion] != 1 {
			t.Errorf("export events = %v (%d rows), want 2 stamps and 1 completion", reasons, len(data.Events))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Invariant: card and stamp inputs are checked at their boundaries before
// anything is written, and a stamp names a card of the staff member's app.
func TestStampInputBounds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	long := strings.Repeat("咖", 101)
	cards := []struct {
		name string
		in   CardInput
		code string
	}{
		{"min stamps_required", CardInput{Name: "a", StampsRequired: 2}, ""},
		{"max stamps_required", CardInput{Name: "a", StampsRequired: 100, MaxStampsPerVisit: intp(100)}, ""},
		{"one stamp required", CardInput{Name: "a", StampsRequired: 1}, "validation_failed"},
		{"101 stamps required", CardInput{Name: "a", StampsRequired: 101}, "validation_failed"},
		{"zero per visit", CardInput{Name: "a", StampsRequired: 5, MaxStampsPerVisit: intp(0)}, "validation_failed"},
		{"101 per visit", CardInput{Name: "a", StampsRequired: 5, MaxStampsPerVisit: intp(101)}, "validation_failed"},
		{"blank name", CardInput{Name: "   ", StampsRequired: 5}, "validation_failed"},
		{"name of 100 runes", CardInput{Name: long[:len(long)-len("咖")], StampsRequired: 5}, ""},
		{"name of 101 runes", CardInput{Name: long, StampsRequired: 5}, "validation_failed"},
		{"NUL in name", CardInput{Name: "a\x00b", StampsRequired: 5}, "validation_failed"},
		{"unknown app", CardInput{Name: "a", StampsRequired: 5}, "not_found"},
	}
	for _, tc := range cards {
		t.Run("card/"+tc.name, func(t *testing.T) {
			app := e.app
			if tc.name == "unknown app" {
				app = uuid.New()
			}
			if _, err := e.svc.CreateCard(ctx, e.owner, app, idem(), tc.in); code(err) != tc.code {
				t.Fatalf("create: %v, want %q", err, tc.code)
			}
		})
	}
	if _, err := e.svc.UpdateCard(ctx, e.owner, uuid.New(), idem(), CardUpdate{Status: strp("paused")}); code(err) != "validation_failed" {
		t.Errorf("bad status: %v", err)
	}

	c := e.card(t, 5, 3, nil)
	m := e.member(t)
	st := e.staff()
	stamps := []struct {
		name  string
		card  uuid.UUID
		count int
		code  string
	}{
		{"zero", c.ID, 0, "validation_failed"},
		{"above the per-operation bound", c.ID, MaxStampsPerOperation + 1, "validation_failed"},
		{"unknown card", uuid.New(), 1, "not_found"},
	}
	for _, tc := range stamps {
		t.Run("stamp/"+tc.name, func(t *testing.T) {
			_, err := e.svc.StaffStamp(ctx, st, idem(), StaffStampInput{ScanToken: e.scan(t, st, m), StampCardID: tc.card, Count: tc.count})
			if code(err) != tc.code {
				t.Fatalf("stamp: %v, want %q", err, tc.code)
			}
		})
	}
	adjusts := []struct {
		name  string
		delta int
		note  string
	}{
		{"delta above the bound", MaxStampsPerOperation + 1, "x"},
		{"delta below the bound", -MaxStampsPerOperation - 1, "x"},
		{"empty note", 1, " "},
		{"note of 201 runes", 1, strings.Repeat("好", 201)},
	}
	for _, tc := range adjusts {
		t.Run("adjust/"+tc.name, func(t *testing.T) {
			if _, err := e.svc.Adjust(ctx, e.owner, m, idem(), c.ID, tc.delta, tc.note); code(err) != "validation_failed" {
				t.Fatalf("adjust: %v, want validation_failed", err)
			}
		})
	}
	if got := e.count(t, "SELECT count(*) FROM stamp_events"); got != 0 {
		t.Errorf("stamp events = %d, want 0", got)
	}
	// A stamp at a store records it on the event.
	store := testutil.NewID()
	e.d.Exec(t, "INSERT INTO stores (id, tenant_id, app_id, name) VALUES ($1, $2, $3, 'Daan')", store, e.tenant, e.app)
	st.StoreID = store
	e.stamp(t, st, m, c.ID, 1)
	err := db.WithTenant(ctx, e.d.App, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		mem, err := access.LoadMember(ctx, sqlc.New(tx), m, e.app)
		if err != nil {
			return err
		}
		data, err := ExportMember(ctx, sqlc.New(tx), mem)
		if err != nil {
			return err
		}
		if len(data.Events) != 1 || data.Events[0].StoreID == nil || *data.Events[0].StoreID != store {
			t.Errorf("events = %+v", data.Events)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
