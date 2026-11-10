package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// phase4Fixture is one tenant with one app, seeded as the owner.
func phase4Fixture(t *testing.T, d *testutil.DB, name string) rlsFixture {
	t.Helper()
	tenant := d.NewTenant(t, name)
	app := testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, $3, $4)", app, tenant, name, "app-"+randHex(3))
	return rlsFixture{d: d, t: t, tenant: tenant, app: app}
}

// Invariant: stamp events and outbound events are append-only for the
// application, even inside the owning tenant's context.
func TestStampAndOutboundEventsAreAppendOnly(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	f := phase4Fixture(t, d, "Alpha")

	stampID, err := f.stampEvent(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := f.outboundEvent(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		stmt string
		id   int64
	}{
		{"UPDATE stamp_events SET delta = 99 WHERE id = $1", stampID},
		{"DELETE FROM stamp_events WHERE id = $1", stampID},
		{"UPDATE outbound_events SET payload = '{\"tampered\":true}' WHERE id = $1", eventID},
		{"DELETE FROM outbound_events WHERE id = $1", eventID},
	}
	for _, tc := range cases {
		err := d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, tc.stmt, tc.id)
			return err
		})
		if db.Code(err) != db.CodeInsufficientPrivilege {
			t.Errorf("%s: got %v, want permission denied", tc.stmt, err)
		}
	}
}

// Invariant: the application updates only the mutable columns of coupons and
// stamp cards; tenant, app, template, member, code and counts of history never move.
func TestPhase4ColumnGrants(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	cases := []struct {
		table, column string
		want          bool
	}{
		{"coupon_templates", "tenant_id", false},
		{"coupon_templates", "app_id", false},
		{"coupon_templates", "type", true},
		{"coupon_templates", "status", true},
		{"coupon_templates", "claimed_count", true},
		{"coupon_instances", "tenant_id", false},
		{"coupon_instances", "app_id", false},
		{"coupon_instances", "member_id", false},
		{"coupon_instances", "template_id", false},
		{"coupon_instances", "code", false},
		{"coupon_instances", "expires_at", false},
		{"coupon_instances", "issued_at", false},
		{"coupon_instances", "status", true},
		{"coupon_instances", "redeemed_at", true},
		{"stamp_cards", "tenant_id", false},
		{"stamp_cards", "app_id", false},
		{"stamp_cards", "stamps_required", true},
		{"stamp_progress", "tenant_id", false},
		{"stamp_progress", "member_id", false},
		{"stamp_progress", "count", true},
		{"stamp_progress", "completions", true},
	}
	for _, role := range []string{migrate.GroupRole, migrate.LoginRole} {
		for _, tc := range cases {
			var got bool
			if err := d.Admin.QueryRow(ctx, "SELECT has_column_privilege($1, $2, $3, 'UPDATE')", role, tc.table, tc.column).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("UPDATE (%s) on %s for %s = %v, want %v", tc.column, tc.table, role, got, tc.want)
			}
		}
	}
}

// Invariant: foreign key checks bypass RLS, so the composite keys must stop a
// row from naming another tenant's app or another app's member, template or card.
func TestPhase4ReferencesStayInsideOneApp(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	a := phase4Fixture(t, d, "Alpha")
	b := phase4Fixture(t, d, "Beta")
	// A second app of tenant A.
	a2 := a
	a2.app = testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Second', 'second')", a2.app, a.tenant)

	memberA, err := a.member(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	templateA, err := a.couponTemplate(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		tenant uuid.UUID
		sql    string
		args   []any
	}{
		{
			name:   "tenant B writes its own row into tenant A's app",
			tenant: b.tenant,
			sql:    "INSERT INTO coupon_templates (tenant_id, app_id, name, type, value) VALUES ($1, $2, 'x', 'percent_off', 10)",
			args:   []any{b.tenant, a.app},
		},
		{
			name:   "tenant B issues a coupon to tenant A's member",
			tenant: b.tenant,
			sql:    "INSERT INTO coupon_instances (tenant_id, app_id, template_id, member_id, code, source, txn_id, issued_at, issued_by_type) VALUES ($1, $2, $3, $4, 'ABCDEF01', 'issue', gen_random_uuid(), now(), 'system')",
			args:   []any{b.tenant, a.app, templateA, memberA},
		},
		{
			name:   "an instance in app 2 names app 1's template and member",
			tenant: a.tenant,
			sql:    "INSERT INTO coupon_instances (tenant_id, app_id, template_id, member_id, code, source, txn_id, issued_at, issued_by_type) VALUES ($1, $2, $3, $4, 'ABCDEF02', 'issue', gen_random_uuid(), now(), 'system')",
			args:   []any{a.tenant, a2.app, templateA, memberA},
		},
		{
			name:   "a card in app 2 rewards with app 1's template",
			tenant: a.tenant,
			sql:    "INSERT INTO stamp_cards (tenant_id, app_id, name, stamps_required, reward_coupon_template_id) VALUES ($1, $2, 'x', 5, $3)",
			args:   []any{a.tenant, a2.app, templateA},
		},
		{
			name:   "an outbound event in app 2 names app 1's member",
			tenant: a.tenant,
			sql:    "INSERT INTO outbound_events (tenant_id, app_id, type, member_id, payload, occurred_at) VALUES ($1, $2, 'member.created', $3, '{}', now())",
			args:   []any{a.tenant, a2.app, memberA},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := d.WithTenant(t, tc.tenant, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tc.sql, tc.args...)
				return err
			})
			if db.Code(err) != db.CodeForeignKeyViolation {
				t.Errorf("got %v, want a foreign key violation", err)
			}
		})
	}
}

// The CHECK constraints reject values the services must never write.
func TestPhase4CheckConstraints(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	f := phase4Fixture(t, d, "Alpha")
	templateID, err := f.couponTemplate(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	memberID, err := f.member(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	cardID, err := f.stampCard(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := "INSERT INTO coupon_templates (tenant_id, app_id, name, type, value, total_limit, per_member_limit, validity_days) VALUES ($1, $2, 'x', $3, $4, $5, $6, $7)"
	inst := "INSERT INTO coupon_instances (tenant_id, app_id, template_id, member_id, code, status, source, txn_id, issued_at, expires_at, issued_by_type) VALUES ($1, $2, $3, $4, $5, $6, 'issue', gen_random_uuid(), $7, $8, 'system')"
	card := "INSERT INTO stamp_cards (tenant_id, app_id, name, stamps_required, max_stamps_per_visit, reward_coupon_template_id) VALUES ($1, $2, 'x', $3, $4, $5)"
	event := "INSERT INTO stamp_events (tenant_id, app_id, member_id, stamp_card_id, txn_id, delta, count_after, completions_after, reason, coupon_instance_id, scan_id, actor_type) VALUES ($1, $2, $3, $4, gen_random_uuid(), $5, 0, $7, $6, NULL, $8, 'system')"
	now := time.Now()

	const notNull = "23502"
	cases := []struct {
		name string
		sql  string
		args []any
		// code is the expected SQLSTATE; empty means accepted.
		code string
	}{
		{"percent 100", tmpl, []any{f.tenant, f.app, "percent_off", 100, nil, 1, nil}, ""},
		{"percent 101", tmpl, []any{f.tenant, f.app, "percent_off", 101, nil, 1, nil}, db.CodeCheckViolation},
		{"percent 0", tmpl, []any{f.tenant, f.app, "percent_off", 0, nil, 1, nil}, db.CodeCheckViolation},
		{"amount 1000000", tmpl, []any{f.tenant, f.app, "amount_off", 1000000, nil, 1, nil}, ""},
		{"amount 0", tmpl, []any{f.tenant, f.app, "amount_off", 0, nil, 1, nil}, db.CodeCheckViolation},
		{"amount 1000001", tmpl, []any{f.tenant, f.app, "amount_off", 1000001, nil, 1, nil}, db.CodeCheckViolation},
		{"amount without value", tmpl, []any{f.tenant, f.app, "amount_off", nil, nil, 1, nil}, notNull},
		{"free item 0", tmpl, []any{f.tenant, f.app, "free_item", 0, nil, 1, nil}, ""},
		{"free item without value", tmpl, []any{f.tenant, f.app, "free_item", nil, nil, 1, nil}, notNull},
		{"free item with value", tmpl, []any{f.tenant, f.app, "free_item", 5, nil, 1, nil}, db.CodeCheckViolation},
		{"unknown type", tmpl, []any{f.tenant, f.app, "bogo", 5, nil, 1, nil}, db.CodeCheckViolation},
		{"zero total limit", tmpl, []any{f.tenant, f.app, "amount_off", 50, 0, 1, nil}, db.CodeCheckViolation},
		{"total limit 10000001", tmpl, []any{f.tenant, f.app, "amount_off", 50, 10000001, 1, nil}, db.CodeCheckViolation},
		{"per-member above total", tmpl, []any{f.tenant, f.app, "amount_off", 50, 1, 5, nil}, ""},
		{"zero per-member limit", tmpl, []any{f.tenant, f.app, "amount_off", 50, nil, 0, nil}, db.CodeCheckViolation},
		{"per-member limit 101", tmpl, []any{f.tenant, f.app, "amount_off", 50, nil, 101, nil}, db.CodeCheckViolation},
		{"no per-member limit", tmpl, []any{f.tenant, f.app, "amount_off", 50, nil, nil, nil}, notNull},
		{"zero validity days", tmpl, []any{f.tenant, f.app, "amount_off", 50, nil, 1, 0}, db.CodeCheckViolation},
		{"lowercase code", inst, []any{f.tenant, f.app, templateID, memberID, "abcdef12", "issued", now, nil}, db.CodeCheckViolation},
		{"short code", inst, []any{f.tenant, f.app, templateID, memberID, "ABC", "issued", now, nil}, db.CodeCheckViolation},
		{"expires before issue", inst, []any{f.tenant, f.app, templateID, memberID, "ABCDEF12", "issued", now, now.Add(-time.Hour)}, db.CodeCheckViolation},
		{"redeemed without redeemed_at", inst, []any{f.tenant, f.app, templateID, memberID, "ABCDEF13", "redeemed", now, nil}, db.CodeCheckViolation},
		{"stored expired status", inst, []any{f.tenant, f.app, templateID, memberID, "ABCDEF14", "expired", now, nil}, db.CodeCheckViolation},
		{"2 stamps required", card, []any{f.tenant, f.app, 2, 1, templateID}, ""},
		{"no reward coupon", card, []any{f.tenant, f.app, 10, 1, nil}, ""},
		{"1 stamp required", card, []any{f.tenant, f.app, 1, 1, templateID}, db.CodeCheckViolation},
		{"101 stamps required", card, []any{f.tenant, f.app, 101, 1, templateID}, db.CodeCheckViolation},
		{"zero per visit", card, []any{f.tenant, f.app, 10, 0, templateID}, db.CodeCheckViolation},
		{"101 per visit", card, []any{f.tenant, f.app, 10, 101, templateID}, db.CodeCheckViolation},
		{"stamp", event, []any{f.tenant, f.app, memberID, cardID, 1, "stamp", 0, "jti-1"}, ""},
		{"stamp without scan", event, []any{f.tenant, f.app, memberID, cardID, 1, "stamp", 0, nil}, db.CodeCheckViolation},
		{"negative stamp", event, []any{f.tenant, f.app, memberID, cardID, -1, "stamp", 0, "jti-1"}, db.CodeCheckViolation},
		{"adjust with scan", event, []any{f.tenant, f.app, memberID, cardID, -1, "adjust", 0, "jti-1"}, db.CodeCheckViolation},
		{"zero delta adjust", event, []any{f.tenant, f.app, memberID, cardID, 0, "adjust", 0, nil}, db.CodeCheckViolation},
		{"completion without reward", event, []any{f.tenant, f.app, memberID, cardID, -10, "completion", 1, nil}, ""},
		{"positive completion", event, []any{f.tenant, f.app, memberID, cardID, 10, "completion", 2, nil}, db.CodeCheckViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tc.sql, tc.args...)
				return err
			})
			switch {
			case tc.code == "" && err != nil:
				t.Errorf("want accepted, got %v", err)
			case tc.code != "" && db.Code(err) != tc.code:
				t.Errorf("got %v, want SQLSTATE %s", err, tc.code)
			}
		})
	}

	t.Run("claimed_count cannot pass total_limit", func(t *testing.T) {
		err := d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE coupon_templates SET total_limit = 1, claimed_count = 2 WHERE id = $1", templateID)
			return err
		})
		if db.Code(err) != db.CodeCheckViolation {
			t.Errorf("got %v, want a check violation", err)
		}
	})
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// The redeem query is the plan 5.5 statement: it succeeds once, never for
// another member or app, and never at or after expires_at.
func TestRedeemCouponInstanceQuery(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	f := phase4Fixture(t, d, "Alpha")
	memberID, err := f.member(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	otherMember, err := f.member(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	templateID, err := f.couponTemplate(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	expires := issued.Add(24 * time.Hour)

	issue := func(t *testing.T, code string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			row, err := sqlc.New(tx).InsertCouponInstance(ctx, sqlc.InsertCouponInstanceParams{
				ID: testutil.NewID(), TenantID: f.tenant, AppID: f.app, TemplateID: templateID, MemberID: memberID,
				Code: code, Source: "issue", TxnID: testutil.NewID(), IssuedAt: ts(issued), ExpiresAt: ts(expires),
				IssuedByType: "system",
			})
			id = row.ID
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	redeem := func(t *testing.T, id, member, app uuid.UUID, now time.Time) error {
		t.Helper()
		return d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			_, err := sqlc.New(tx).RedeemCouponInstance(ctx, sqlc.RedeemCouponInstanceParams{
				Now: ts(now), AppID: app, ID: id, MemberID: member,
			})
			return err
		})
	}

	t.Run("once only", func(t *testing.T) {
		id := issue(t, "REDEEM01")
		if err := redeem(t, id, otherMember, f.app, issued); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("another member's redeem: %v, want no rows", err)
		}
		if err := redeem(t, id, memberID, testutil.NewID(), issued); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("another app's redeem: %v, want no rows", err)
		}
		if err := redeem(t, id, memberID, f.app, issued); err != nil {
			t.Fatalf("first redeem: %v", err)
		}
		if err := redeem(t, id, memberID, f.app, issued); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("second redeem: %v, want no rows", err)
		}
	})

	t.Run("expiry boundary", func(t *testing.T) {
		id := issue(t, "REDEEM02")
		if err := redeem(t, id, memberID, f.app, expires); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("redeem at expires_at: %v, want no rows", err)
		}
		if err := redeem(t, id, memberID, f.app, expires.Add(-time.Microsecond)); err != nil {
			t.Errorf("redeem just before expires_at: %v", err)
		}
	})

	t.Run("effective status", func(t *testing.T) {
		issue(t, "REDEEM03")
		var rows []sqlc.ListMemberCouponsRow
		state := "expired"
		available := "available"
		err := d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) (err error) {
			rows, err = sqlc.New(tx).ListMemberCoupons(ctx, sqlc.ListMemberCouponsParams{
				Now: ts(expires), AppID: f.app, MemberID: memberID, State: &state, PageLimit: 50,
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Code != "REDEEM03" || rows[0].State != "expired" {
			t.Errorf("expired coupons at expires_at = %+v, want only REDEEM03", rows)
		}
		// Just before expiry the same coupon is available and redeemable.
		var redeemable []sqlc.ListRedeemableMemberCouponsRow
		err = d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) (err error) {
			q := sqlc.New(tx)
			before := ts(expires.Add(-time.Second))
			if rows, err = q.ListMemberCoupons(ctx, sqlc.ListMemberCouponsParams{
				Now: before, AppID: f.app, MemberID: memberID, State: &available, PageLimit: 50,
			}); err != nil {
				return err
			}
			redeemable, err = q.ListRedeemableMemberCoupons(ctx, sqlc.ListRedeemableMemberCouponsParams{
				Now: before, AppID: f.app, MemberID: memberID, PageLimit: 50,
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Code != "REDEEM03" || len(redeemable) != 1 || redeemable[0].Code != "REDEEM03" {
			t.Errorf("available before expiry = %+v, redeemable = %+v, want only REDEEM03", rows, redeemable)
		}
	})
}

// A completion is recorded once per completion number and a reward instance
// pays for one completion only.
func TestStampCompletionRecordedOnce(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	f := phase4Fixture(t, d, "Alpha")
	memberID, err := f.member(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	cardID, err := f.stampCard(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	rewardA, err := f.couponInstance(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	rewardB, err := f.couponInstance(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(reward uuid.UUID, completionsAfter int32) error {
		return d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			_, err := sqlc.New(tx).InsertStampEvent(ctx, sqlc.InsertStampEventParams{
				TenantID: f.tenant, AppID: f.app, MemberID: memberID, StampCardID: cardID, TxnID: testutil.NewID(),
				Delta: -10, CountAfter: 0, CompletionsAfter: completionsAfter, Reason: "completion",
				CouponInstanceID: uuid.NullUUID{UUID: reward, Valid: reward != uuid.Nil}, ActorType: "staff",
			})
			return err
		})
	}
	if err := insert(rewardA, 1); err != nil {
		t.Fatal(err)
	}
	if err := insert(rewardB, 1); db.Code(err) != db.CodeUniqueViolation {
		t.Errorf("second completion number 1: %v, want a unique violation", err)
	}
	if err := insert(rewardA, 2); db.Code(err) != db.CodeUniqueViolation {
		t.Errorf("reward reused: %v, want a unique violation", err)
	}
	if err := insert(rewardB, 2); err != nil {
		t.Errorf("completion number 2: %v", err)
	}
	// A card without a reward completes with no instance.
	if err := insert(uuid.Nil, 3); err != nil {
		t.Errorf("completion without reward: %v", err)
	}
}

// Stamps of one scan on one card are summed for max_stamps_per_visit;
// adjustments, completions, other scans and other cards do not count.
func TestSumScanStamps(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	f := phase4Fixture(t, d, "Alpha")
	memberID, err := f.member(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	cardID, err := f.stampCard(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	otherCard, err := f.stampCard(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	ins := "INSERT INTO stamp_events (tenant_id, app_id, member_id, stamp_card_id, txn_id, delta, count_after, completions_after, reason, scan_id, actor_type) VALUES ($1, $2, $3, $4, gen_random_uuid(), $5, 1, $8, $6, $7, 'staff')"
	for i, r := range []struct {
		card   uuid.UUID
		delta  int
		reason string
		scan   any
	}{
		{cardID, 2, "stamp", "scan-a"},
		{cardID, 1, "stamp", "scan-a"},
		{cardID, -3, "completion", "scan-a"},
		{cardID, 4, "stamp", "scan-b"},
		{otherCard, 5, "stamp", "scan-a"},
		{cardID, 1, "adjust", nil},
	} {
		d.Exec(t, ins, f.tenant, f.app, memberID, r.card, r.delta, r.reason, r.scan, i)
	}
	var got int32
	err = d.WithTenant(t, f.tenant, func(ctx context.Context, tx pgx.Tx) (err error) {
		got, err = sqlc.New(tx).SumScanStamps(ctx, sqlc.SumScanStampsParams{AppID: f.app, StampCardID: cardID, ScanID: ptr("scan-a")})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("stamps on scan-a = %d, want 3", got)
	}
}

func ptr[T any](v T) *T { return &v }

// Every migration has a working Down: roll the schema back to nothing and
// apply it again on a fresh database.
func TestMigrationsRollBackAndReapply(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	sqlDB, err := sql.Open("pgx", d.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	top, err := migrate.Version(ctx, sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	for v := top; v > 0; v-- {
		if err := migrate.Down(ctx, sqlDB); err != nil {
			t.Fatalf("down from %d: %v", v, err)
		}
	}
	if v, err := migrate.Version(ctx, sqlDB); err != nil || v != 0 {
		t.Fatalf("version after rolling back = %d (%v), want 0", v, err)
	}
	if err := migrate.Up(ctx, sqlDB); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if v, err := migrate.Version(ctx, sqlDB); err != nil || v != top {
		t.Fatalf("version after reapplying = %d (%v), want %d", v, err, top)
	}
}
