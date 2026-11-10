package stamp

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/coupon"
)

// Invariant: the daily loyalty reconciliation finds a clean ledger clean,
// and detects planted drift in stamp counts, completions and a coupon
// template's claimed_count. Every run is recorded, details hold IDs and
// counts only, and mismatches log an ERROR. It runs as the worker role.
func TestLoyaltyReconciliationDetectsDrift(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	reward := e.template(t, coupon.TemplateInput{})
	c := e.card(t, 2, 5, &reward.ID)
	m, other := e.member(t), e.member(t)
	st := e.staff()
	e.stamp(t, st, m, c.ID, 3) // one completion, one reward, count 1
	e.stamp(t, st, other, c.ID, 1)
	// The adjustment completes the other member's card: a second reward.
	if _, err := e.svc.Adjust(ctx, e.owner, other, idem(), c.ID, 1, "fix"); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	worker := New(Deps{Pool: e.d.Worker, Clock: e.clk, Log: slog.New(slog.NewTextHandler(&buf, nil)), StaffWritesPerMinute: -1})
	checked, mismatches, err := worker.Reconcile(ctx, e.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Two progress rows and one template.
	if checked != 3 || mismatches != 0 {
		t.Fatalf("clean run: checked %d, mismatches %d", checked, mismatches)
	}

	// Drift: a progress count, a completion count and a template counter
	// changed behind the ledger's back.
	e.d.Exec(t, "UPDATE stamp_progress SET count = count + 1 WHERE member_id = $1", m)
	e.d.Exec(t, "UPDATE stamp_progress SET completions = completions + 1 WHERE member_id = $1", other)
	e.d.Exec(t, "UPDATE coupon_templates SET claimed_count = claimed_count + 2 WHERE id = $1", reward.ID)
	buf.Reset()
	checked, mismatches, err = worker.Reconcile(ctx, e.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if checked != 3 || mismatches != 3 {
		t.Fatalf("drifted run: checked %d, mismatches %d, want 3 and 3", checked, mismatches)
	}
	if !strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "mismatches=3") {
		t.Errorf("drift must log an ERROR: %s", buf.String())
	}

	var runs, recorded, progress, templates int
	var details string
	if err := e.d.Admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM loyalty_reconciliation_runs WHERE finished_at IS NOT NULL),
		mismatches, progress_checked, templates_checked, details::text
		FROM loyalty_reconciliation_runs ORDER BY id DESC LIMIT 1`).Scan(&runs, &recorded, &progress, &templates, &details); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || recorded != 3 || progress != 2 || templates != 1 {
		t.Errorf("runs %d, last run: mismatches %d, progress %d, templates %d", runs, recorded, progress, templates)
	}
	for _, want := range []string{m.String(), other.String(), reward.ID.String(), `"kind": "stamp_progress"`, `"kind": "coupon_template"`, `"event_sum": 1`, `"claimed_count": 4`, `"instances": 2`} {
		if !strings.Contains(details, want) {
			t.Errorf("details lack %s: %s", want, details)
		}
	}
	if strings.Contains(details, "fix") || strings.Contains(details, "line_user") {
		t.Errorf("details hold more than IDs and counts: %s", details)
	}
}
