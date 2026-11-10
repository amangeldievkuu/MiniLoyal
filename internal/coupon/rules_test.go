package coupon

import (
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
)

// Rules that need no database: validation, expiry, issuability, codes.

func valid() draft {
	return draft{name: "NT$50 off", typ: TypeAmountOff, value: intp(50), perMemberLimit: 1, status: "active"}
}

func TestDraftValidate(t *testing.T) {
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		edit  func(d *draft)
		field string // "" for valid
	}{
		{"valid amount_off", func(*draft) {}, ""},
		{"percent_off 100", func(d *draft) { d.typ, d.value = TypePercentOff, intp(100) }, ""},
		{"percent_off above 100", func(d *draft) { d.typ, d.value = TypePercentOff, intp(101) }, "value"},
		{"amount_off without value", func(d *draft) { d.value = nil }, "value"},
		{"amount_off above the maximum", func(d *draft) { d.value = intp(1_000_001) }, "value"},
		{"free_item without value", func(d *draft) { d.typ, d.value = TypeFreeItem, nil }, ""},
		{"free_item with a value", func(d *draft) { d.typ = TypeFreeItem }, "value"},
		{"unknown type", func(d *draft) { d.typ = "bogo" }, "type"},
		{"empty name", func(d *draft) { d.name = "" }, "name"},
		{"name of 101 characters", func(d *draft) { d.name = string(make([]rune, 101)) }, "name"},
		{"per member above total", func(d *draft) { d.totalLimit, d.perMemberLimit = intp(2), 3 }, "per_member_limit"},
		{"per member equal to total", func(d *draft) { d.totalLimit, d.perMemberLimit = intp(3), 3 }, ""},
		{"per member zero", func(d *draft) { d.perMemberLimit = 0 }, "per_member_limit"},
		{"total limit zero", func(d *draft) { d.totalLimit = intp(0) }, "total_limit"},
		{"window ends at its start", func(d *draft) { d.validFrom, d.validTo = &from, &from }, "valid_to"},
		{"validity days zero", func(d *draft) { d.validityDays = intp(0) }, "validity_days"},
		{"bad status", func(d *draft) { d.status = "deleted" }, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := valid()
			tc.edit(&d)
			fields := d.validate()
			if tc.field == "" {
				if len(fields) != 0 {
					t.Fatalf("fields = %v, want none", fields)
				}
				return
			}
			if _, ok := fields[tc.field]; !ok {
				t.Fatalf("fields = %v, want %s", fields, tc.field)
			}
		})
	}
}

func TestExpiryOf(t *testing.T) {
	issued := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: issued.Add(d), Valid: true} }
	days := func(n int32) *int32 { return &n }
	cases := []struct {
		name string
		tmpl sqlc.CouponTemplate
		want *time.Duration
	}{
		{"neither", sqlc.CouponTemplate{}, nil},
		{"validity days only", sqlc.CouponTemplate{ValidityDays: days(7)}, durp(7 * 24 * time.Hour)},
		{"valid_to only", sqlc.CouponTemplate{ValidTo: at(time.Hour)}, durp(time.Hour)},
		{"valid_to earlier wins", sqlc.CouponTemplate{ValidityDays: days(7), ValidTo: at(48 * time.Hour)}, durp(48 * time.Hour)},
		{"validity days earlier wins", sqlc.CouponTemplate{ValidityDays: days(1), ValidTo: at(48 * time.Hour)}, durp(24 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expiryOf(tc.tmpl, issued)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("expiry = %s, want none", got)
			case tc.want != nil && (got == nil || !got.Equal(issued.Add(*tc.want))):
				t.Fatalf("expiry = %v, want %s", got, issued.Add(*tc.want))
			}
		})
	}
}

func durp(d time.Duration) *time.Duration { return &d }

func TestIssuable(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: now.Add(d), Valid: true} }
	limit := func(n int32) *int32 { return &n }
	base := sqlc.CouponTemplate{Status: "active", Claimable: true}
	cases := []struct {
		name   string
		edit   func(t *sqlc.CouponTemplate)
		source string
		want   error
	}{
		{"claimable claim", func(*sqlc.CouponTemplate) {}, SourceClaim, nil},
		{"issue-only template refuses a claim", func(t *sqlc.CouponTemplate) { t.Claimable = false }, SourceClaim, errNotClaimable},
		{"issue-only template takes an issue", func(t *sqlc.CouponTemplate) { t.Claimable = false }, SourceIssue, nil},
		{"issue-only template takes a reward", func(t *sqlc.CouponTemplate) { t.Claimable = false }, SourceReward, nil},
		{"archived", func(t *sqlc.CouponTemplate) { t.Status = "archived" }, SourceIssue, errNotClaimable},
		{"before valid_from", func(t *sqlc.CouponTemplate) { t.ValidFrom = ts(time.Second) }, SourceIssue, errNotClaimable},
		{"at valid_from", func(t *sqlc.CouponTemplate) { t.ValidFrom = ts(0) }, SourceIssue, nil},
		{"at valid_to", func(t *sqlc.CouponTemplate) { t.ValidTo = ts(0) }, SourceReward, errNotClaimable},
		{"just before valid_to", func(t *sqlc.CouponTemplate) { t.ValidTo = ts(time.Microsecond) }, SourceReward, nil},
		{"sold out", func(t *sqlc.CouponTemplate) { t.TotalLimit, t.ClaimedCount = limit(3), 3 }, SourceReward, errSoldOut},
		{"one left", func(t *sqlc.CouponTemplate) { t.TotalLimit, t.ClaimedCount = limit(3), 2 }, SourceClaim, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := base
			tc.edit(&tmpl)
			if got := issuable(tmpl, tc.source, now); got != tc.want {
				t.Fatalf("issuable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodesMatchTheSchema(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9A-Z]{6,12}$`)
	seen := map[string]bool{}
	for range 10_000 {
		c, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(c) {
			t.Fatalf("code %q does not match the schema", c)
		}
		for _, r := range "ILOU" {
			for _, got := range c {
				if got == r {
					t.Fatalf("code %q has the ambiguous letter %c", c, r)
				}
			}
		}
		if seen[c] {
			t.Fatalf("duplicate code %q in 10,000", c)
		}
		seen[c] = true
	}
}

func TestStateOf(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	exp := func(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: now.Add(d), Valid: true} }
	cases := []struct {
		status  string
		expires pgtype.Timestamptz
		want    string
	}{
		{StatusIssued, pgtype.Timestamptz{}, StateAvailable},
		{StatusIssued, exp(time.Microsecond), StateAvailable},
		{StatusIssued, exp(0), StateExpired},
		{StatusIssued, exp(-time.Hour), StateExpired},
		{StatusRedeemed, exp(-time.Hour), StatusRedeemed},
		{StatusVoided, exp(time.Hour), StatusVoided},
	}
	for _, tc := range cases {
		if got := stateOf(tc.status, tc.expires, now); got != tc.want {
			t.Errorf("stateOf(%s, %v) = %s, want %s", tc.status, tc.expires.Time, got, tc.want)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 123456000, time.UTC)
	id := uuid.New()
	gotAt, gotID, err := decodeCursor(encodeCursor(at, id))
	if err != nil || !gotAt.Time.Equal(at) || gotID.UUID != id {
		t.Fatalf("round trip = %v %v %v", gotAt.Time, gotID.UUID, err)
	}
	for _, bad := range []string{"!", "bm9waXBl", "MjAyNi0xMS0xMFQwOTowMDowMFp8bm90LWEtdXVpZA"} {
		if _, _, err := decodeCursor(bad); code(err) != "bad_request" {
			t.Errorf("decodeCursor(%q) = %v, want bad_request", bad, err)
		}
	}
}

func TestMergeKeepsAndClears(t *testing.T) {
	cur := sqlc.CouponTemplate{
		ID: uuid.New(), AppID: uuid.New(), Name: "NT$50 off", Type: TypeAmountOff, Value: int32p(50),
		TotalLimit: int32p(10), PerMemberLimit: 1, Claimable: true, Status: "active",
	}
	free := TypeFreeItem
	cases := []struct {
		name    string
		in      TemplateUpdate
		cleared map[string]bool
		check   func(t *testing.T, d draft, p sqlc.UpdateCouponTemplateParams)
	}{
		{"name only keeps the rest and writes claimable", TemplateUpdate{Name: strp(" Latte ")}, nil, func(t *testing.T, d draft, p sqlc.UpdateCouponTemplateParams) {
			if d.name != "Latte" || p.Claimable == nil || !*p.Claimable || p.SetValue || p.SetTotalLimit || *d.value != 50 {
				t.Fatalf("draft %+v params %+v", d, p)
			}
		}},
		{"switch to free_item drops the value", TemplateUpdate{Type: &free}, nil, func(t *testing.T, d draft, p sqlc.UpdateCouponTemplateParams) {
			if d.value != nil || !p.SetValue || p.Value != nil || len(d.validate()) != 0 {
				t.Fatalf("draft %+v params %+v fields %v", d, p, d.validate())
			}
		}},
		{"switch to free_item with a value is invalid", TemplateUpdate{Type: &free, Value: intp(5)}, nil, func(t *testing.T, d draft, _ sqlc.UpdateCouponTemplateParams) {
			if _, ok := d.validate()["value"]; !ok {
				t.Fatalf("validate = %v, want value", d.validate())
			}
		}},
		{"clear total limit", TemplateUpdate{}, map[string]bool{"total_limit": true}, func(t *testing.T, d draft, p sqlc.UpdateCouponTemplateParams) {
			if d.totalLimit != nil || !p.SetTotalLimit || p.TotalLimit != nil {
				t.Fatalf("draft %+v params %+v", d, p)
			}
		}},
		{"claimable off", TemplateUpdate{Claimable: boolp(false)}, nil, func(t *testing.T, d draft, p sqlc.UpdateCouponTemplateParams) {
			if d.claimable || p.Claimable == nil || *p.Claimable {
				t.Fatalf("draft %+v params %+v", d, p)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleared := tc.cleared
			if cleared == nil {
				cleared = map[string]bool{}
			}
			d, p := merge(cur, tc.in, cleared)
			tc.check(t, d, p)
		})
	}
}

func int32p(v int32) *int32 { return &v }
func strp(v string) *string { return &v }
