package migrations_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// countApps counts rows visible to the current transaction.
func countApps(ctx context.Context, tx pgx.Tx) (int, error) {
	var n int
	err := tx.QueryRow(ctx, "SELECT count(*) FROM apps").Scan(&n)
	return n, err
}

func TestTenantIsolationOnApps(t *testing.T) {
	d := testutil.NewDB(t)
	tenantA := d.NewTenant(t, "Alpha")
	tenantB := d.NewTenant(t, "Beta")

	appID := testutil.NewID()
	err := d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", appID, tenantA)
		return err
	})
	if err != nil {
		t.Fatalf("insert as tenant A: %v", err)
	}

	cases := []struct {
		name   string
		tenant uuid.UUID
		want   int
	}{
		{"own tenant sees its app", tenantA, 1},
		{"other tenant sees nothing", tenantB, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			err := d.WithTenant(t, tc.tenant, func(ctx context.Context, tx pgx.Tx) (err error) {
				got, err = countApps(ctx, tx)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("no tenant context sees nothing", func(t *testing.T) {
		var got int
		err := d.App.QueryRow(context.Background(), "SELECT count(*) FROM apps").Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Errorf("count without context = %d, want 0", got)
		}
	})

	t.Run("system context sees everything", func(t *testing.T) {
		var got int
		err := d.WithSystem(t, func(ctx context.Context, tx pgx.Tx) (err error) {
			got, err = countApps(ctx, tx)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != 1 {
			t.Errorf("system count = %d, want 1", got)
		}
	})
}

func TestWithCheckRejectsForeignTenantRows(t *testing.T) {
	d := testutil.NewDB(t)
	tenantA := d.NewTenant(t, "Alpha")
	tenantB := d.NewTenant(t, "Beta")

	err := d.WithTenant(t, tenantB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Sneaky', 'sneaky')", testutil.NewID(), tenantA)
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Fatalf("insert for another tenant: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
	}

	// Updates cannot move a row to another tenant either.
	appID := testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", appID, tenantA)
	err = d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE apps SET tenant_id = $2 WHERE id = $1", appID, tenantB)
		return err
	})
	if db.Code(err) != db.CodeInsufficientPrivilege {
		t.Fatalf("move row to another tenant: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
	}
}

func TestAuditLogIsAppendOnlyForTheApplication(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")

	var id int64
	err := d.WithTenant(t, tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			"INSERT INTO audit_log (tenant_id, actor_type, action, target_type) VALUES ($1, 'system', 'test.insert', 'test') RETURNING id",
			tenant).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, stmt := range []string{
		"UPDATE audit_log SET action = 'tampered' WHERE id = $1",
		"DELETE FROM audit_log WHERE id = $1",
	} {
		err := d.WithTenant(t, tenant, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, id)
			return err
		})
		if db.Code(err) != db.CodeInsufficientPrivilege {
			t.Errorf("%s: got %v, want permission denied", stmt, err)
		}
	}
}

func TestApplicationRoleCannotBypassRLS(t *testing.T) {
	d := testutil.NewDB(t)
	var super, bypass bool
	err := d.App.QueryRow(context.Background(),
		"SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&super, &bypass)
	if err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Fatalf("application role must not be superuser (%v) or bypass RLS (%v)", super, bypass)
	}

	// Policies are forced, so even the table owner would be subject to them.
	rows, err := d.Admin.Query(context.Background(),
		"SELECT relname FROM pg_class WHERE relkind = 'r' AND relnamespace = 'public'::regnamespace AND relname NOT IN ('goose_db_version', 'users', 'refresh_tokens', 'totp_recovery_codes', 'mfa_challenges', 'security_events') AND relname NOT LIKE 'river\\_%' AND NOT (relrowsecurity AND relforcerowsecurity)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var unprotected []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		unprotected = append(unprotected, name)
	}
	if len(unprotected) > 0 {
		t.Errorf("tables without forced row-level security: %v", unprotected)
	}
}

func TestWithTenantRejectsNilTenant(t *testing.T) {
	d := testutil.NewDB(t)
	err := d.WithTenant(t, uuid.Nil, func(context.Context, pgx.Tx) error { return nil })
	if err == nil {
		t.Fatal("expected an error for the nil tenant")
	}
}

// ---- every tenant table, data-driven

// execer is satisfied by pgx.Tx and *pgxpool.Pool, so the same insert runs
// as the owner (fixtures) and as the application (attacks).
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// rlsFixture holds tenant A's dependencies for the inserts below.
type rlsFixture struct {
	d      *testutil.DB
	t      *testing.T
	tenant uuid.UUID
	app    uuid.UUID
}

// user inserts a fresh dashboard user as the owner and returns its ID.
func (f rlsFixture) user() uuid.UUID {
	id := testutil.NewID()
	f.d.Exec(f.t, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')", id, randHex(6)+"@example.com")
	return id
}

// channelEnv hands out the three channel environments in turn, so repeated
// inserts for one app do not collide on (app_id, environment).
var channelSeq atomic.Int32

func channelEnv() string {
	return []string{"developing", "review", "published"}[int(channelSeq.Add(1)-1)%3]
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// tenantTable describes one tenant-scoped table.
type tenantTable struct {
	name string
	// col is the column that scopes rows to a tenant.
	col string
	// insert adds one row that belongs to f.tenant. For tenants itself the
	// row's id is a fresh tenant, since a tenant cannot own another row.
	insert func(ctx context.Context, x execer, f rlsFixture) error
	// seeded is false when tenant A's row exists before the test (tenants).
	seeded bool
	// appDelete is true when the application role may DELETE from the table.
	appDelete bool
}

var rlsTables = []tenantTable{
	{name: "tenants", col: "id", insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO tenants (id, type, name, slug) VALUES ($1, 'brand', 'Sneaky', $2)", testutil.NewID(), "sneaky-"+randHex(3))
		return err
	}},
	{name: "tenant_users", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'staff')", f.tenant, f.user())
		return err
	}},
	{name: "apps", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'App', $3)", testutil.NewID(), f.tenant, "app-"+randHex(3))
		return err
	}},
	{name: "app_channels", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO app_channels (id, tenant_id, app_id, environment, channel_id, liff_id) VALUES ($1, $2, $3, $4, '2007000001', $5)",
			testutil.NewID(), f.tenant, f.app, channelEnv(), "2007000001-"+randHex(4))
		return err
	}},
	{name: "stores", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO stores (id, tenant_id, app_id, name) VALUES ($1, $2, $3, 'Main')", testutil.NewID(), f.tenant, f.app)
		return err
	}},
	{name: "staff", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO staff (id, tenant_id, app_id, display_name) VALUES ($1, $2, $3, 'Mei')", testutil.NewID(), f.tenant, f.app)
		return err
	}},
	{name: "api_keys", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO api_keys (id, tenant_id, app_id, name, prefix, key_hash, scopes) VALUES ($1, $2, $3, 'pos', 'ml_abcdefgh', $4, '{members:read}')",
			testutil.NewID(), f.tenant, f.app, randHex(32))
		return err
	}},
	{name: "audit_log", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO audit_log (tenant_id, app_id, actor_type, action, target_type) VALUES ($1, $2, 'system', 'test', 'test')", f.tenant, f.app)
		return err
	}},
	{name: "idempotency_keys", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, expires_at) VALUES ($1, 'user:x', $2, '\\x00', now() + interval '1 day')", f.tenant, randHex(8))
		return err
	}},
	{name: "members", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", testutil.NewID(), f.tenant, f.app, "U"+randHex(16))
		return err
	}},
	{name: "member_consents", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		memberID := testutil.NewID()
		if _, err := x.Exec(ctx, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", memberID, f.tenant, f.app, "U"+randHex(16)); err != nil {
			return err
		}
		_, err := x.Exec(ctx, "INSERT INTO member_consents (tenant_id, app_id, member_id, version) VALUES ($1, $2, $3, '2026-11')", f.tenant, f.app, memberID)
		return err
	}},
	{name: "webhook_inbound_events", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO webhook_inbound_events (tenant_id, app_id, line_webhook_event_id, event_type) VALUES ($1, $2, $3, 'follow')", f.tenant, f.app, randHex(12))
		return err
	}},
	{name: "invitations", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO invitations (id, tenant_id, email, role, token_hash, expires_at) VALUES ($1, $2, $3, 'staff', $4, now() + interval '7 days')", testutil.NewID(), f.tenant, randHex(6)+"@example.com", randHex(32))
		return err
	}},
	{name: "points_rules", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		// One rule per app: a fresh app keeps the insert from colliding.
		appID := testutil.NewID()
		if _, err := x.Exec(ctx, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Rules', $3)", appID, f.tenant, "rules-"+randHex(3)); err != nil {
			return err
		}
		_, err := x.Exec(ctx, "INSERT INTO points_rules (app_id, tenant_id) VALUES ($1, $2)", appID, f.tenant)
		return err
	}},
	{name: "points_accounts", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		memberID := testutil.NewID()
		if _, err := x.Exec(ctx, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", memberID, f.tenant, f.app, "U"+randHex(16)); err != nil {
			return err
		}
		_, err := x.Exec(ctx, "INSERT INTO points_accounts (member_id, tenant_id, app_id) VALUES ($1, $2, $3)", memberID, f.tenant, f.app)
		return err
	}},
	{name: "points_lots", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		memberID := testutil.NewID()
		if _, err := x.Exec(ctx, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", memberID, f.tenant, f.app, "U"+randHex(16)); err != nil {
			return err
		}
		_, err := x.Exec(ctx, "INSERT INTO points_lots (tenant_id, app_id, member_id, earned, remaining) VALUES ($1, $2, $3, 10, 10)", f.tenant, f.app, memberID)
		return err
	}},
	{name: "points_ledger", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		memberID := testutil.NewID()
		if _, err := x.Exec(ctx, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", memberID, f.tenant, f.app, "U"+randHex(16)); err != nil {
			return err
		}
		_, err := x.Exec(ctx, "INSERT INTO points_ledger (tenant_id, app_id, member_id, txn_id, delta, balance_after, reason, actor_type) VALUES ($1, $2, $3, $4, 5, 5, 'adjust', 'system')", f.tenant, f.app, memberID, testutil.NewID())
		return err
	}},
	{name: "scan_token_uses", col: "tenant_id", seeded: true, appDelete: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := x.Exec(ctx, "INSERT INTO scan_token_uses (jti, tenant_id, app_id, used_at) VALUES ($1, $2, $3, now())", randHex(16), f.tenant, f.app)
		return err
	}},
	// Phase 4. Templates and cards are archived, never deleted; instances are
	// voided; stamp events and outbound events are append-only.
	{name: "coupon_templates", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := f.couponTemplate(ctx, x)
		return err
	}},
	{name: "coupon_instances", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := f.couponInstance(ctx, x)
		return err
	}},
	{name: "stamp_cards", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := f.stampCard(ctx, x)
		return err
	}},
	{name: "stamp_progress", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		memberID, err := f.member(ctx, x)
		if err != nil {
			return err
		}
		cardID, err := f.stampCard(ctx, x)
		if err != nil {
			return err
		}
		_, err = x.Exec(ctx, "INSERT INTO stamp_progress (member_id, stamp_card_id, tenant_id, app_id, count) VALUES ($1, $2, $3, $4, 1)", memberID, cardID, f.tenant, f.app)
		return err
	}},
	{name: "stamp_events", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := f.stampEvent(ctx, x)
		return err
	}},
	{name: "outbound_events", col: "tenant_id", seeded: true, insert: func(ctx context.Context, x execer, f rlsFixture) error {
		_, err := f.outboundEvent(ctx, x)
		return err
	}},
}

// member inserts a member of f.app through x and returns its ID.
func (f rlsFixture) member(ctx context.Context, x execer) (uuid.UUID, error) {
	id := testutil.NewID()
	_, err := x.Exec(ctx, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", id, f.tenant, f.app, "U"+randHex(16))
	return id, err
}

func (f rlsFixture) couponTemplate(ctx context.Context, x execer) (uuid.UUID, error) {
	id := testutil.NewID()
	_, err := x.Exec(ctx, "INSERT INTO coupon_templates (id, tenant_id, app_id, name, type, value) VALUES ($1, $2, $3, 'NT$50 off', 'amount_off', 50)", id, f.tenant, f.app)
	return id, err
}

func (f rlsFixture) couponInstance(ctx context.Context, x execer) (uuid.UUID, error) {
	memberID, err := f.member(ctx, x)
	if err != nil {
		return uuid.Nil, err
	}
	templateID, err := f.couponTemplate(ctx, x)
	if err != nil {
		return uuid.Nil, err
	}
	id := testutil.NewID()
	_, err = x.Exec(ctx, "INSERT INTO coupon_instances (id, tenant_id, app_id, template_id, member_id, code, source, txn_id, issued_at, issued_by_type) VALUES ($1, $2, $3, $4, $5, $6, 'issue', $7, now(), 'system')",
		id, f.tenant, f.app, templateID, memberID, strings.ToUpper(randHex(5)), testutil.NewID())
	return id, err
}

func (f rlsFixture) stampCard(ctx context.Context, x execer) (uuid.UUID, error) {
	templateID, err := f.couponTemplate(ctx, x)
	if err != nil {
		return uuid.Nil, err
	}
	id := testutil.NewID()
	_, err = x.Exec(ctx, "INSERT INTO stamp_cards (id, tenant_id, app_id, name, stamps_required, reward_coupon_template_id) VALUES ($1, $2, $3, 'Coffee', 10, $4)", id, f.tenant, f.app, templateID)
	return id, err
}

func (f rlsFixture) stampEvent(ctx context.Context, x execer) (int64, error) {
	memberID, err := f.member(ctx, x)
	if err != nil {
		return 0, err
	}
	cardID, err := f.stampCard(ctx, x)
	if err != nil {
		return 0, err
	}
	var id int64
	err = queryRow(ctx, x, "INSERT INTO stamp_events (tenant_id, app_id, member_id, stamp_card_id, txn_id, delta, count_after, completions_after, reason, actor_type) VALUES ($1, $2, $3, $4, $5, 1, 1, 0, 'stamp', 'system') RETURNING id",
		f.tenant, f.app, memberID, cardID, testutil.NewID()).Scan(&id)
	return id, err
}

func (f rlsFixture) outboundEvent(ctx context.Context, x execer) (int64, error) {
	memberID, err := f.member(ctx, x)
	if err != nil {
		return 0, err
	}
	var id int64
	err = queryRow(ctx, x, "INSERT INTO outbound_events (tenant_id, app_id, type, member_id, payload, occurred_at) VALUES ($1, $2, 'member.created', $3, '{}', now()) RETURNING id",
		f.tenant, f.app, memberID).Scan(&id)
	return id, err
}

// queryRow runs a single-row query on a pgx.Tx or a pool.
func queryRow(ctx context.Context, x execer, sql string, args ...any) pgx.Row {
	return x.(interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}).QueryRow(ctx, sql, args...)
}

// TestEveryTenantTableIsListed keeps rlsTables in step with the schema: a
// new tenant table fails here until it gets an entry.
func TestEveryTenantTableIsListed(t *testing.T) {
	d := testutil.NewDB(t)
	rows, err := d.Admin.Query(context.Background(),
		"SELECT relname FROM pg_class WHERE relkind = 'r' AND relnamespace = 'public'::regnamespace AND relrowsecurity ORDER BY relname")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	listed := map[string]bool{}
	for _, tbl := range rlsTables {
		listed[tbl.name] = true
	}
	// System-only tables carry RLS that admits system mode alone; they have
	// no tenant column and are tested in TestSystemOnlyTables.
	for _, name := range systemOnlyTables {
		listed[name] = true
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !listed[name] {
			t.Errorf("table %s has row-level security but no entry in rlsTables", name)
		}
		delete(listed, name)
	}
	for name := range listed {
		t.Errorf("rlsTables entry %s is not a table with row-level security", name)
	}
}

// Invariant: in every tenant table, tenant A's rows are invisible from
// tenant B's context and from no context; a row claiming tenant A cannot be
// inserted, moved or deleted from tenant B's context.
func TestEveryTenantTableIsIsolated(t *testing.T) {
	d := testutil.NewDB(t)
	tenantA := d.NewTenant(t, "Alpha")
	tenantB := d.NewTenant(t, "Beta")
	appA := testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", appA, tenantA)
	f := rlsFixture{d: d, t: t, tenant: tenantA, app: appA}
	ctx := context.Background()

	countAs := func(t *testing.T, tbl tenantTable, run func(fn db.TxFunc) error) int {
		t.Helper()
		var n int
		err := run(func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s: %v", tbl.name, err)
		}
		return n
	}
	ownerCount := func(t *testing.T, tbl tenantTable) int {
		t.Helper()
		var n int
		if err := d.Admin.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, tbl := range rlsTables {
		t.Run(tbl.name, func(t *testing.T) {
			if tbl.seeded {
				if err := tbl.insert(ctx, d.Admin, f); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			before := ownerCount(t, tbl)
			if before == 0 {
				t.Fatal("fixture missing")
			}

			inA := countAs(t, tbl, func(fn db.TxFunc) error { return d.WithTenant(t, tenantA, fn) })
			inB := countAs(t, tbl, func(fn db.TxFunc) error { return d.WithTenant(t, tenantB, fn) })
			system := countAs(t, tbl, func(fn db.TxFunc) error { return d.WithSystem(t, fn) })
			var noCtx int
			if err := d.App.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA).Scan(&noCtx); err != nil {
				t.Fatal(err)
			}
			if inA != before || system != before {
				t.Errorf("own context sees %d, system sees %d, want %d", inA, system, before)
			}
			if inB != 0 || noCtx != 0 {
				t.Errorf("tenant B sees %d, no context sees %d, want 0", inB, noCtx)
			}

			// Insert claiming tenant A from tenant B's context.
			err := d.WithTenant(t, tenantB, func(ctx context.Context, tx pgx.Tx) error { return tbl.insert(ctx, tx, f) })
			if db.Code(err) != db.CodeInsufficientPrivilege {
				t.Errorf("cross-tenant insert: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
			}
			// The same insert from tenant A's own context is allowed (unless
			// the table is one the application never inserts into this way).
			if tbl.name != "tenants" {
				if err := d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error { return tbl.insert(ctx, tx, f) }); err != nil {
					t.Errorf("own-tenant insert: %v", err)
				}
				before++
			}

			// Moving tenant A's rows to tenant B from A's context.
			if tbl.col == "tenant_id" {
				err := d.WithTenant(t, tenantA, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET tenant_id = $2 WHERE tenant_id = $1", tbl.name), tenantA, tenantB)
					return err
				})
				if db.Code(err) != db.CodeInsufficientPrivilege {
					t.Errorf("move to another tenant: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
				}
			}

			// Deleting tenant A's rows from tenant B's context: either the
			// rows are invisible (0 affected) or the role lacks DELETE.
			err = d.WithTenant(t, tenantB, func(ctx context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s = $1", tbl.name, tbl.col), tenantA)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 0 {
					return fmt.Errorf("deleted %d rows of tenant A", tag.RowsAffected())
				}
				return nil
			})
			switch {
			case tbl.appDelete && err != nil:
				t.Errorf("cross-tenant delete: %v", err)
			case !tbl.appDelete && db.Code(err) != db.CodeInsufficientPrivilege:
				t.Errorf("delete without privilege: got %v, want SQLSTATE %s", err, db.CodeInsufficientPrivilege)
			}

			if after := ownerCount(t, tbl); after != before {
				t.Errorf("tenant A rows went from %d to %d", before, after)
			}
		})
	}
}

// Invariant: the application roles hold exactly the DML grants the schema
// intends. Append-only tables cannot be changed or emptied; API keys are
// revoked, never deleted; nothing may be truncated.
func TestApplicationRoleGrants(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()

	cases := []struct {
		table, priv string
		want        bool
	}{
		{"audit_log", "SELECT", true},
		{"audit_log", "INSERT", true},
		{"audit_log", "UPDATE", false},
		{"audit_log", "DELETE", false},
		{"api_keys", "SELECT", true},
		{"api_keys", "INSERT", true},
		{"api_keys", "UPDATE", true},
		{"api_keys", "DELETE", false},
		{"tenants", "DELETE", false},
		{"tenants", "UPDATE", true},
		{"idempotency_keys", "DELETE", true},
		{"refresh_tokens", "DELETE", true},
		{"goose_db_version", "SELECT", false},
		{"goose_db_version", "INSERT", false},
		// Phase 2. Members are anonymized in place, never deleted; consents
		// and security events are append-only; invitations are revoked, not
		// deleted; webhook dedup rows are swept but never edited.
		{"members", "SELECT", true},
		{"members", "INSERT", true},
		{"members", "UPDATE", true},
		{"members", "DELETE", false},
		{"points_ledger", "UPDATE", false},
		{"points_ledger", "DELETE", false},
		{"points_lots", "DELETE", false},
		{"points_accounts", "DELETE", false},
		{"member_consents", "SELECT", true},
		{"member_consents", "INSERT", true},
		{"member_consents", "UPDATE", false},
		{"member_consents", "DELETE", false},
		{"invitations", "SELECT", true},
		{"invitations", "INSERT", true},
		{"invitations", "UPDATE", true},
		{"invitations", "DELETE", false},
		{"webhook_inbound_events", "SELECT", true},
		{"webhook_inbound_events", "INSERT", true},
		{"webhook_inbound_events", "UPDATE", false},
		{"webhook_inbound_events", "DELETE", true},
		{"security_events", "SELECT", true},
		{"security_events", "INSERT", true},
		{"security_events", "UPDATE", false},
		{"security_events", "DELETE", false},
		// Phase 4. Templates, cards and instances are archived or voided,
		// never deleted; their tenant and app never change. Stamp events and
		// outbound events are append-only.
		{"coupon_templates", "SELECT", true},
		{"coupon_templates", "INSERT", true},
		{"coupon_templates", "DELETE", false},
		{"coupon_instances", "SELECT", true},
		{"coupon_instances", "INSERT", true},
		{"coupon_instances", "DELETE", false},
		{"stamp_cards", "SELECT", true},
		{"stamp_cards", "INSERT", true},
		{"stamp_cards", "DELETE", false},
		{"stamp_progress", "SELECT", true},
		{"stamp_progress", "INSERT", true},
		{"stamp_progress", "DELETE", false},
		{"stamp_events", "SELECT", true},
		{"stamp_events", "INSERT", true},
		{"stamp_events", "UPDATE", false},
		{"stamp_events", "DELETE", false},
		{"outbound_events", "SELECT", true},
		{"outbound_events", "INSERT", true},
		{"outbound_events", "UPDATE", false},
		{"outbound_events", "DELETE", false},
	}
	for _, role := range []string{migrate.GroupRole, migrate.LoginRole} {
		for _, tc := range cases {
			var got bool
			if err := d.Admin.QueryRow(ctx, "SELECT has_table_privilege($1, $2, $3)", role, tc.table, tc.priv).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("%s on %s for %s = %v, want %v", tc.priv, tc.table, role, got, tc.want)
			}
		}
	}

	// No table grants TRUNCATE, TRIGGER or REFERENCES to the application.
	rows, err := d.Admin.Query(ctx, "SELECT relname FROM pg_class WHERE relkind = 'r' AND relnamespace = 'public'::regnamespace")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		for _, priv := range []string{"TRUNCATE", "TRIGGER", "REFERENCES"} {
			var got bool
			if err := d.Admin.QueryRow(ctx, "SELECT has_table_privilege($1, $2, $3)", migrate.LoginRole, table, priv).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got {
				t.Errorf("%s may %s %s", migrate.LoginRole, priv, table)
			}
		}
	}
	// The application owns nothing, so it cannot ALTER a table or drop a policy.
	var owned int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner WHERE c.relnamespace = 'public'::regnamespace AND r.rolname IN ($1, $2)", migrate.GroupRole, migrate.LoginRole).Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if owned != 0 {
		t.Errorf("application roles own %d relations", owned)
	}
}

// systemOnlyTables have row-level security that only system mode passes.
var systemOnlyTables = []string{"points_reconciliation_runs"}
