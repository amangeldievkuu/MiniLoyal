package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

// Invariant: zero, negative and out-of-range amounts are refused with 400
// at the API boundary on every points write, and refused writes add no
// ledger row.
func TestPointsAmountsAreValidatedAtTheBoundary(t *testing.T) {
	w := newPointsWorld(t)
	member := "/v1/dashboard/members/" + w.member.String() + "/points/adjustments"
	server := "/v1/members/" + w.member.String() + "/points"
	if r := w.call(http.MethodPost, member, "bearer:owner", uuid.NewString(), map[string]any{"delta": 50, "note": "seed"}); r.status != http.StatusOK {
		t.Fatalf("seed: %d %s", r.status, r.body)
	}
	long := strings.Repeat("x", 201)

	cases := []struct {
		name, path, auth string
		body             func() map[string]any
	}{
		{"earn spend 0", "/v1/staff/points/earn", "bearer:cashier", func() map[string]any {
			return map[string]any{"scan_token": w.scan(w.cashier, w.member), "spend_amount": 0}
		}},
		{"earn spend -1", "/v1/staff/points/earn", "bearer:cashier", func() map[string]any {
			return map[string]any{"scan_token": w.scan(w.cashier, w.member), "spend_amount": -1}
		}},
		{"earn spend below one unit", "/v1/staff/points/earn", "bearer:cashier", func() map[string]any {
			return map[string]any{"scan_token": w.scan(w.cashier, w.member), "spend_amount": 99}
		}},
		{"earn spend and points", "/v1/staff/points/earn", "bearer:manager", func() map[string]any {
			return map[string]any{"scan_token": w.scan(w.manager, w.member), "spend_amount": 500, "points": 5}
		}},
		{"earn points 0", "/v1/staff/points/earn", "bearer:manager", func() map[string]any { return map[string]any{"scan_token": w.scan(w.manager, w.member), "points": 0} }},
		{"earn points -5", "/v1/staff/points/earn", "bearer:manager", func() map[string]any { return map[string]any{"scan_token": w.scan(w.manager, w.member), "points": -5} }},
		{"earn points max+1", "/v1/staff/points/earn", "bearer:manager", func() map[string]any {
			return map[string]any{"scan_token": w.scan(w.manager, w.member), "points": 1_000_001}
		}},
		{"earn note too long", "/v1/staff/points/earn", "bearer:cashier", func() map[string]any {
			return map[string]any{"scan_token": w.scan(w.cashier, w.member), "spend_amount": 500, "note": long}
		}},
		{"redeem 0", "/v1/staff/points/redeem", "bearer:cashier", func() map[string]any { return map[string]any{"scan_token": w.scan(w.cashier, w.member), "points": 0} }},
		{"redeem -3", "/v1/staff/points/redeem", "bearer:cashier", func() map[string]any { return map[string]any{"scan_token": w.scan(w.cashier, w.member), "points": -3} }},
		{"redeem max+1", "/v1/staff/points/redeem", "bearer:cashier", func() map[string]any {
			return map[string]any{"scan_token": w.scan(w.cashier, w.member), "points": 1_000_001}
		}},
		{"adjust 0", member, "bearer:owner", func() map[string]any { return map[string]any{"delta": 0, "note": "zero"} }},
		{"adjust max+1", member, "bearer:owner", func() map[string]any { return map[string]any{"delta": 1_000_001, "note": "big"} }},
		{"adjust -(max+1)", member, "bearer:owner", func() map[string]any { return map[string]any{"delta": -1_000_001, "note": "big"} }},
		{"adjust blank note", member, "bearer:owner", func() map[string]any { return map[string]any{"delta": 5, "note": "   "} }},
		{"adjust note too long", member, "bearer:owner", func() map[string]any { return map[string]any{"delta": 5, "note": long} }},
		{"server earn points 0", server, "key:key-write", func() map[string]any { return map[string]any{"type": "earn", "points": 0} }},
		{"server earn points -1", server, "key:key-write", func() map[string]any { return map[string]any{"type": "earn", "points": -1} }},
		{"server earn spend -1", server, "key:key-write", func() map[string]any { return map[string]any{"type": "earn", "spend_amount": -1} }},
		{"server earn nothing", server, "key:key-write", func() map[string]any { return map[string]any{"type": "earn"} }},
		{"server redeem 0", server, "key:key-write", func() map[string]any { return map[string]any{"type": "redeem", "points": 0} }},
		{"server redeem -5", server, "key:key-write", func() map[string]any { return map[string]any{"type": "redeem", "points": -5} }},
		{"server redeem with spend", server, "key:key-write", func() map[string]any { return map[string]any{"type": "redeem", "points": 1, "spend_amount": 100} }},
		{"server adjust 0", server, "key:key-write", func() map[string]any { return map[string]any{"type": "adjust", "points": 0} }},
		{"server adjust max+1", server, "key:key-write", func() map[string]any { return map[string]any{"type": "adjust", "points": 1_000_001} }},
		{"server ref_id too long", server, "key:key-write", func() map[string]any {
			return map[string]any{"type": "earn", "points": 1, "ref_id": strings.Repeat("r", 129)}
		}},
	}
	before := w.ledgerCount()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := w.call(http.MethodPost, c.path, c.auth, uuid.NewString(), c.body())
			if r.status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", r.status, r.body)
			}
			if code := r.code(t); code != "validation_failed" && code != "bad_request" {
				t.Errorf("code = %s", code)
			}
		})
	}
	if got := w.ledgerCount(); got != before {
		t.Fatalf("refused writes added %d ledger rows", got-before)
	}
}

func (w *pointsWorld) ledgerCount() int {
	w.t.Helper()
	var n int
	if err := w.d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM points_ledger").Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// Invariant: spend_amount is at most 100,000,000 (the spec maximum) and is
// stored exactly as sent. A larger spend is refused with 400; it is never
// accepted, wrapped into another value, or answered with 500.
func TestSpendAboveTheSpecMaximumIsRefused(t *testing.T) {
	w := newPointsWorld(t)
	rules := map[string]any{"currency": "TWD", "spend_per_point": 100, "points_per_unit": 1, "min_spend": 0, "max_points_per_txn": 10, "enabled": true}
	if r := w.call(http.MethodPut, "/v1/dashboard/apps/"+w.app.String()+"/points-rules", "bearer:owner", uuid.NewString(), rules); r.status != http.StatusOK {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	server := "/v1/members/" + w.member.String() + "/points"
	// The spec maximum itself is accepted and earns the cap.
	ok := w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:cashier", uuid.NewString(), map[string]any{"scan_token": w.scan(w.cashier, w.member), "spend_amount": 100_000_000})
	var tx api.PointsTransaction
	ok.into(t, &tx)
	if ok.status != http.StatusOK || tx.Points != 10 || tx.SpendAmount == nil || *tx.SpendAmount != 100_000_000 {
		t.Fatalf("spend at the maximum: %d %s", ok.status, ok.body)
	}

	var defects []string
	for _, spend := range []int64{100_000_001, 1 << 31, 1<<32 + 100} {
		for _, via := range []string{"staff", "server"} {
			var r presp
			if via == "staff" {
				r = w.call(http.MethodPost, "/v1/staff/points/earn", "bearer:cashier", uuid.NewString(), map[string]any{"scan_token": w.scan(w.cashier, w.member), "spend_amount": spend})
			} else {
				r = w.call(http.MethodPost, server, "key:key-write", uuid.NewString(), map[string]any{"type": "earn", "spend_amount": spend})
			}
			if r.status == http.StatusBadRequest {
				continue
			}
			detail := fmt.Sprintf("%s spend %d -> %d", via, spend, r.status)
			if r.status == http.StatusOK {
				var got api.PointsTransaction
				r.into(t, &got)
				var stored int64
				if err := w.d.Admin.QueryRow(context.Background(), "SELECT spend_amount FROM points_ledger WHERE txn_id = $1", uuid.UUID(got.Id)).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				detail += fmt.Sprintf(" (stored spend_amount %d)", stored)
			}
			defects = append(defects, detail)
		}
	}
	if len(defects) > 0 {
		t.Fatalf("regression of security review Phase 3, F5: spend_amount above the spec maximum of 100000000 is not refused: %s. "+
			"internal/points/rules.go PointsFor checks only spend >= 0, and service.go apply stores int32(*op.spend), "+
			"so values above 2^31-1 wrap (500 on the CHECK, or a different stored amount)", strings.Join(defects, "; "))
	}
}
