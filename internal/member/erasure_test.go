package member_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/member"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

// Phase 4 review: a PDPA erasure blanks the free text staff typed about the
// member (stamp adjustment notes and coupon void reasons) and keeps the
// rows themselves, completion markers and other members' notes. The
// erasure's audit row holds IDs only. Both the dashboard erasure and the
// member's own request do this.
func TestErasureBlanksStaffNotes(t *testing.T) {
	for _, self := range []bool{false, true} {
		name := "dashboard"
		if self {
			name = "self"
		}
		t.Run(name, func(t *testing.T) { erasureBlanksNotes(t, self) })
	}
}

func erasureBlanksNotes(t *testing.T, self bool) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyring := crypto.Static(bytes.Repeat([]byte{5}, 32))
	svc := member.New(member.Deps{Pool: d.App, Tenants: tenant.New(d.App, keyring, clock.Real{}, log), Keyring: keyring, Log: log})

	tenantID := d.NewTenant(t, "Alpha")
	app := testutil.NewID()
	d.Exec(t, "INSERT INTO apps (id, tenant_id, name, slug) VALUES ($1, $2, 'Cafe', 'cafe')", app, tenantID)
	owner := testutil.NewID()
	d.Exec(t, "INSERT INTO users (id, email, password_hash) VALUES ($1, 'owner@example.com', 'x')", owner)
	d.Exec(t, "INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'owner')", tenantID, owner)
	m, other := testutil.NewID(), testutil.NewID()
	for _, id := range []uuid.UUID{m, other} {
		d.Exec(t, "INSERT INTO members (id, tenant_id, app_id, line_user_id) VALUES ($1, $2, $3, $4)", id, tenantID, app, "U"+id.String())
	}
	tmpl, card := testutil.NewID(), testutil.NewID()
	d.Exec(t, "INSERT INTO coupon_templates (id, tenant_id, app_id, name, type) VALUES ($1, $2, $3, 'Coffee', 'free_item')", tmpl, tenantID, app)
	d.Exec(t, "INSERT INTO stamp_cards (id, tenant_id, app_id, name, stamps_required) VALUES ($1, $2, $3, 'Card', 5)", card, tenantID, app)
	for i, id := range []uuid.UUID{m, other} {
		d.Exec(t, `INSERT INTO stamp_events (tenant_id, app_id, member_id, stamp_card_id, txn_id, delta, count_after, completions_after, reason, note, actor_type)
			VALUES ($1, $2, $3, $4, gen_random_uuid(), 1, 1, 0, 'adjust', 'phoned Ms Lin 0912-000-111', 'user')`, tenantID, app, id, card)
		d.Exec(t, `INSERT INTO coupon_instances (tenant_id, app_id, template_id, member_id, code, status, source, txn_id, issued_at, issued_by_type, voided_at, voided_by_type, void_reason)
			VALUES ($1, $2, $3, $4, $5, 'voided', 'issue', gen_random_uuid(), now(), 'user', now(), 'user', 'Ms Lin complained')`, tenantID, app, tmpl, id, "CODE000"+string(rune('A'+i)))
	}
	d.Exec(t, `INSERT INTO stamp_events (tenant_id, app_id, member_id, stamp_card_id, txn_id, delta, count_after, completions_after, reason, note, actor_type)
		VALUES ($1, $2, $3, $4, gen_random_uuid(), -5, 0, 1, 'completion', 'reward_unavailable', 'system')`, tenantID, app, m, card)

	var err error
	if self {
		err = svc.EraseSelf(ctx, principal.Principal{Kind: principal.KindMember, MemberID: m, AppID: app, TenantID: tenantID})
	} else {
		err = svc.Erase(ctx, principal.Principal{Kind: principal.KindUser, UserID: owner}, m)
	}
	if err != nil {
		t.Fatalf("erase: %v", err)
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Admin.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count("SELECT count(*) FROM stamp_events WHERE member_id = $1 AND reason = 'adjust' AND note IS NULL", m); n != 1 {
		t.Errorf("erased member's adjustment notes left: want 1 blank row, got %d", n)
	}
	if n := count("SELECT count(*) FROM stamp_events WHERE member_id = $1 AND reason = 'completion' AND note = 'reward_unavailable'", m); n != 1 {
		t.Errorf("completion marker lost: %d", n)
	}
	if n := count("SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND status = 'voided' AND void_reason IS NULL AND voided_at IS NOT NULL", m); n != 1 {
		t.Errorf("erased member's void reason left or void lost: %d", n)
	}
	if n := count("SELECT count(*) FROM stamp_events WHERE member_id = $1 AND note IS NOT NULL", other) +
		count("SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND void_reason IS NOT NULL", other); n != 2 {
		t.Errorf("another member's notes changed: %d of 2 left", n)
	}
	var audit string
	if err := d.Admin.QueryRow(ctx, "SELECT coalesce(string_agg(metadata::text, ' '), '') FROM audit_log WHERE target_id = $1", m.String()).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(audit, "Lin") || strings.Contains(audit, "0912") {
		t.Errorf("audit rows hold the erased text: %s", audit)
	}
	if !self && count("SELECT count(*) FROM audit_log WHERE action = 'member.erase' AND target_id = $1", m.String()) != 1 {
		t.Error("dashboard erasure not audited")
	}
}
