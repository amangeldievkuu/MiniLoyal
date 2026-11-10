package httpserver

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

// The PDPA export carries the member's points summary and full history, in
// one snapshot, past the export page size. The member's own copy drops the
// brand's notes and references as /v1/me/points/history does; coupons and
// stamps are present as empty lists until their module fills them.
func TestMemberExportCarriesPoints(t *testing.T) {
	w := newPointsWorld(t)
	memberPath := "/v1/dashboard/members/" + w.member.String()

	// 520 bulk transactions push the history past one export page (500).
	const bulk = 520
	w.d.Exec(t, `INSERT INTO points_ledger (tenant_id, app_id, member_id, txn_id, delta, balance_after, reason, note, ref_type, ref_id, actor_type, created_at)
		SELECT $1, $2, $3, gen_random_uuid(), 1, g, 'adjust', 'bulk', 'import', 'r-' || g, 'system', now() - interval '1 day' + g * interval '1 second'
		FROM generate_series(1, $4::int) g`, w.tenant, w.app, w.member, bulk)
	w.d.Exec(t, "INSERT INTO points_accounts (member_id, tenant_id, app_id, balance, lifetime_earned) VALUES ($1, $2, $3, $4, $4)", w.member, w.tenant, w.app, bulk)
	if r := w.call(http.MethodPost, memberPath+"/points/adjustments", "bearer:admin", "export-adj", map[string]any{"delta": 30, "note": "goodwill"}); r.status != http.StatusOK {
		t.Fatalf("adjust: %d %s", r.status, r.body)
	}

	tests := []struct {
		name      string
		path      string
		auth      string
		wantNotes bool
	}{
		{"dashboard owner", memberPath + "/export", "bearer:owner", true},
		{"member self", "/v1/me/export", "bearer:member", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := w.call(http.MethodGet, tc.path, tc.auth, "", nil)
			if r.status != http.StatusOK {
				t.Fatalf("export: %d %s", r.status, r.body)
			}
			var got api.MemberExport
			r.into(t, &got)
			if got.Points.Summary.Balance != bulk+30 || got.Points.Summary.MemberId.String() != w.member.String() {
				t.Errorf("summary = %+v", got.Points.Summary)
			}
			if len(got.Points.History) != bulk+1 {
				t.Fatalf("history has %d transactions, want %d", len(got.Points.History), bulk+1)
			}
			newest := got.Points.History[0]
			if newest.Points != 30 || newest.BalanceAfter != bulk+30 {
				t.Errorf("newest = %+v", newest)
			}
			for i := 1; i < len(got.Points.History); i++ {
				if want := int64(bulk - i + 1); got.Points.History[i].BalanceAfter != want {
					t.Fatalf("history[%d].balance_after = %d, want %d (newest first, no gaps)", i, got.Points.History[i].BalanceAfter, want)
				}
			}
			if hasNote := newest.Note != nil && *newest.Note == "goodwill"; hasNote != tc.wantNotes {
				t.Errorf("note = %v, want present %v", newest.Note, tc.wantNotes)
			}
			if hasRef := got.Points.History[1].RefId != nil; hasRef != tc.wantNotes {
				t.Errorf("ref_id = %v, want present %v", got.Points.History[1].RefId, tc.wantNotes)
			}
			for _, field := range []string{`"coupons":[]`, `"progress":[]`, `"events":[]`} {
				if !bytes.Contains(r.body, []byte(field)) {
					t.Errorf("export lacks %s", field)
				}
			}
		})
	}

	// Another tenant's owner and a staff-role user get nothing.
	if r := w.call(http.MethodGet, memberPath+"/export", "bearer:outsider", "", nil); r.status != http.StatusNotFound {
		t.Errorf("outsider export: %d %s", r.status, r.body)
	}
	if r := w.call(http.MethodGet, memberPath+"/export", "bearer:staff-user", "", nil); r.status != http.StatusForbidden {
		t.Errorf("staff-role export: %d %s", r.status, r.body)
	}
}
