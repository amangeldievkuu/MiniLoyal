package httpserver

import (
	"bytes"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/card"
)

// HTTP-level behaviour of coupons and stamps that the coverage matrices do
// not show: what a scan returns, the PDPA export, and the counter flow.

func couponIDs(items []api.CouponInstance) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	for _, c := range items {
		out = append(out, uuid.UUID(c.Id))
	}
	return out
}

// Invariant: the scan shows staff exactly the member's redeemable coupons
// and their progress on every active card; one scan can redeem several
// coupons; a redeemed or voided coupon leaves the next scan.
func TestScanShowsCouponsAndStamps(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	voided := issueCoupon(t, w, "owner-a", uuid.UUID(f.member.Member.Id), f.template)
	want(t, w.as("owner-a").post("/v1/dashboard/coupons/"+voided.String()+"/void", uuid.NewString(), map[string]any{"reason": "test"}), http.StatusOK)
	// Another member's coupon is never on this member's scan.
	others := issueCoupon(t, w, "owner-a", uuid.UUID(f.second.Member.Id), f.template)

	scan := func() api.ScanResult {
		r := w.call(http.MethodPost, "/v1/staff/scan", f.staffSess.SessionToken, "", "", map[string]any{"qr": f.qrOf(f.member)})
		want(t, r, http.StatusOK)
		var res api.ScanResult
		r.into(t, &res)
		return res
	}
	res := scan()
	ids := couponIDs(res.Coupons)
	if len(ids) != 2 || !slices.Contains(ids, f.coupon) || !slices.Contains(ids, f.spareCoupon) || slices.Contains(ids, others) {
		t.Fatalf("scan coupons = %v, want %s and %s", ids, f.coupon, f.spareCoupon)
	}
	for _, c := range res.Coupons {
		if c.State != api.CouponStateAvailable || c.Template.Name != "Latte coupon" {
			t.Errorf("scan coupon = %+v", c)
		}
	}
	if len(res.Stamps) != 1 || uuid.UUID(res.Stamps[0].StampCardId) != f.card || res.Stamps[0].Count != 1 || res.Stamps[0].Reward == nil {
		t.Fatalf("scan stamps = %+v", res.Stamps)
	}

	for _, id := range []uuid.UUID{f.coupon, f.spareCoupon} {
		r := w.call(http.MethodPost, "/v1/staff/coupons/"+id.String()+"/redeem", f.staffSess.SessionToken, "", uuid.NewString(), map[string]any{"scan_token": res.ScanToken})
		want(t, r, http.StatusOK)
		var c api.CouponInstance
		r.into(t, &c)
		// The manager belongs to no store, so none is recorded.
		if c.State != api.CouponStateRedeemed || c.RedeemedAt == nil || c.Store != nil {
			t.Errorf("redeemed = %+v", c)
		}
	}
	if again := scan(); len(again.Coupons) != 0 {
		t.Errorf("after redeeming both, the scan still lists %v", couponIDs(again.Coupons))
	}
}

// Invariant: the PDPA export carries every coupon (voided and redeemed
// included), progress on every card and every stamp event, for the
// dashboard and for the member themselves.
func TestExportCarriesCouponsAndStamps(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	want(t, w.as("owner-a").post("/v1/dashboard/coupons/"+f.spareCoupon.String()+"/void", uuid.NewString(), map[string]any{"reason": "test"}), http.StatusOK)
	scanTok := f.scanAs(f.staffSess.SessionToken, f.member)
	want(t, w.call(http.MethodPost, "/v1/staff/stamps", f.staffSess.SessionToken, "", uuid.NewString(),
		map[string]any{"scan_token": scanTok, "stamp_card_id": f.card, "count": 1}), http.StatusOK)
	// Archived cards with progress stay in the export.
	want(t, w.as("owner-a").patch(stampCardPath(f), uuid.NewString(), map[string]any{"status": "archived"}), http.StatusOK)

	for name, r := range map[string]tresp{
		"dashboard": w.as("owner-a").get(memberPath(f) + "/export"),
		"member":    w.call(http.MethodGet, "/v1/me/export", f.member.SessionToken, "", "", nil),
	} {
		t.Run(name, func(t *testing.T) {
			want(t, r, http.StatusOK)
			var got api.MemberExport
			r.into(t, &got)
			states := map[uuid.UUID]api.CouponState{}
			for _, c := range got.Coupons {
				states[uuid.UUID(c.Id)] = c.State
			}
			if len(states) != 2 || states[f.coupon] != api.CouponStateAvailable || states[f.spareCoupon] != api.CouponStateVoided {
				t.Errorf("coupons = %v", states)
			}
			if len(got.Stamps.Progress) != 1 || got.Stamps.Progress[0].Count != 2 || got.Stamps.Progress[0].Status != api.ArchiveStatusArchived {
				t.Errorf("progress = %+v", got.Stamps.Progress)
			}
			if len(got.Stamps.Events) != 2 || got.Stamps.Events[0].Reason != api.StampEventReasonStamp || got.Stamps.Events[1].Reason != api.StampEventReasonAdjust {
				t.Errorf("events = %+v, want the stamp then the opening adjustment", got.Stamps.Events)
			}
			if bytes.Contains(r.body, []byte("opening stamp")) {
				t.Errorf("the export carries the brand's adjustment note")
			}
		})
	}
}

// Invariant: total_limit cannot drop below claimed_count; the answer is 422
// validation_failed naming total_limit, and the template is unchanged.
func TestTemplateLimitBelowClaimedIs422(t *testing.T) {
	w := newWorld(t)
	f := newMatrixFixtures(t, w)
	r := w.as("owner-a").patch(templatePath(f), uuid.NewString(), map[string]any{"total_limit": 1})
	want(t, r, http.StatusUnprocessableEntity)
	var e api.Error
	r.into(t, &e)
	if e.Code != "validation_failed" || e.Details == nil || (*e.Details)["total_limit"] == nil {
		t.Fatalf("error = %+v", e)
	}
	r = w.as("owner-a").patch(templatePath(f), uuid.NewString(), map[string]any{"value": 80})
	want(t, r, http.StatusConflict)
	if code := r.errCode(t); code != "coupon_template_in_use" {
		t.Errorf("code = %s", code)
	}
	got := w.as("owner-a").get(templatePath(f))
	want(t, got, http.StatusOK)
	var tmpl api.CouponTemplate
	got.into(t, &tmpl)
	if tmpl.TotalLimit != nil || tmpl.Value == nil || *tmpl.Value != 50 || tmpl.ClaimedCount != 2 {
		t.Errorf("template = %+v", tmpl)
	}
}

// Invariant: a coupon redeem retried with the same key replays even after
// the scan expired; a new redeem with the expired scan is scan_invalid; a
// fresh scan is a new body, so the old key is a mismatch.
func TestCouponRedeemAcrossTheScanLifetime(t *testing.T) {
	e := newMemberEnv(t)
	owner := e.owner("owner@example.com")
	app := e.app(owner)
	st := e.staffInvite(owner, app, "Mei")
	staff := e.staffSession(app, "Umei", st.InviteCode)
	m := e.lineLogin(app, "Ucoupon", "Lin")

	var tmpl api.CouponTemplate
	e.must(e.do(http.MethodPost, "/v1/dashboard/apps/"+app.Id.String()+"/coupon-templates",
		map[string]any{"name": "Free cookie", "type": "free_item", "validity_days": 1}, idemHeaders(owner)), http.StatusCreated, "template").into(t, &tmpl)
	issue := func() uuid.UUID {
		var c api.CouponInstance
		e.must(e.do(http.MethodPost, "/v1/dashboard/members/"+m.Member.Id.String()+"/coupons",
			map[string]any{"template_id": tmpl.Id}, idemHeaders(owner)), http.StatusCreated, "issue").into(t, &c)
		if c.ExpiresAt == nil || !c.ExpiresAt.Equal(e.clk.Now().Add(24*time.Hour)) {
			t.Fatalf("expires_at = %v", c.ExpiresAt)
		}
		return uuid.UUID(c.Id)
	}
	first, second := issue(), issue()
	redeem := func(id uuid.UUID, scanToken, key string) resp {
		h := bearer(staff.SessionToken)
		h["Idempotency-Key"] = key
		return e.do(http.MethodPost, "/v1/staff/coupons/"+id.String()+"/redeem", map[string]any{"scan_token": scanToken}, h)
	}

	tok := e.scanToken(staff.SessionToken, m.SessionToken)
	key := uuid.NewString()
	done := e.must(redeem(first, tok, key), http.StatusOK, "redeem")
	e.clk.Advance(card.ScanTokenTTL + time.Second)
	late := e.must(redeem(first, tok, key), http.StatusOK, "replay after the scan expired")
	if !bytes.Equal(done.body, late.body) {
		t.Errorf("replay differs:\n%s\n%s", done.body, late.body)
	}
	wantCode(t, redeem(second, tok, uuid.NewString()), http.StatusForbidden, "scan_invalid")
	fresh := e.scanToken(staff.SessionToken, m.SessionToken)
	wantCode(t, redeem(first, fresh, key), http.StatusUnprocessableEntity, "idempotency_mismatch")
	e.must(redeem(second, fresh, uuid.NewString()), http.StatusOK, "second coupon on a fresh scan")
	// Expired coupons cannot be redeemed, with the same one answer.
	third := issue()
	e.clk.Advance(24 * time.Hour)
	// Sessions are shorter than a day: sign both back in.
	m = e.lineLogin(app, "Ucoupon", "Lin")
	staff = e.staffSession(app, "Umei", "")
	wantCode(t, redeem(third, e.scanToken(staff.SessionToken, m.SessionToken), uuid.NewString()), http.StatusConflict, "coupon_unavailable")
}
