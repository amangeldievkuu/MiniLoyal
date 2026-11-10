package stamp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
)

// loyaltyMismatch is one entry of a loyalty reconciliation run's details:
// IDs and counts only.
type loyaltyMismatch struct {
	Kind           string     `json:"kind"`
	AppID          uuid.UUID  `json:"app_id"`
	MemberID       *uuid.UUID `json:"member_id,omitempty"`
	StampCardID    *uuid.UUID `json:"stamp_card_id,omitempty"`
	Count          *int64     `json:"count,omitempty"`
	EventSum       *int64     `json:"event_sum,omitempty"`
	Completions    *int64     `json:"completions,omitempty"`
	CompletionRows *int64     `json:"completion_rows,omitempty"`
	TemplateID     *uuid.UUID `json:"template_id,omitempty"`
	ClaimedCount   *int64     `json:"claimed_count,omitempty"`
	Instances      *int64     `json:"instances,omitempty"`
}

// maxDetails bounds what one run records; the mismatch count stays exact.
const maxDetails = 100

// Reconcile checks the cached loyalty counters against their append-only
// history, across every tenant in system mode. For each member and card,
// stamp_progress.count must equal the sum of the stamp events and
// completions the number of completion rows. For each coupon template
// (stamp rewards draw on them too), claimed_count must equal the coupons
// issued from it. The run is recorded in loyalty_reconciliation_runs and
// mismatches are logged at ERROR, which is the alert. It returns how many
// progress rows and templates were checked and how many disagreed.
func (s *Service) Reconcile(ctx context.Context, now time.Time) (checked int, mismatches int, err error) {
	err = db.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		runID, err := q.StartLoyaltyReconciliationRun(ctx, ts(now))
		if err != nil {
			return fmt.Errorf("start run: %w", err)
		}
		progress, err := q.CountStampProgress(ctx)
		if err != nil {
			return fmt.Errorf("count progress: %w", err)
		}
		templates, err := q.CountCouponTemplates(ctx)
		if err != nil {
			return fmt.Errorf("count templates: %w", err)
		}
		stampRows, err := q.ReconcileStampProgress(ctx)
		if err != nil {
			return fmt.Errorf("reconcile stamps: %w", err)
		}
		couponRows, err := q.ReconcileCouponTemplates(ctx)
		if err != nil {
			return fmt.Errorf("reconcile coupons: %w", err)
		}
		details := make([]loyaltyMismatch, 0, min(len(stampRows)+len(couponRows), maxDetails))
		for _, r := range stampRows {
			if len(details) == maxDetails {
				break
			}
			details = append(details, loyaltyMismatch{
				Kind: "stamp_progress", AppID: r.AppID, MemberID: &r.MemberID, StampCardID: &r.StampCardID,
				Count: &r.Count, EventSum: &r.EventSum, Completions: &r.Completions, CompletionRows: &r.CompletionRows,
			})
		}
		for _, r := range couponRows {
			if len(details) == maxDetails {
				break
			}
			details = append(details, loyaltyMismatch{
				Kind: "coupon_template", AppID: r.AppID, TemplateID: &r.ID, ClaimedCount: &r.ClaimedCount, Instances: &r.Instances,
			})
		}
		raw, err := json.Marshal(details)
		if err != nil {
			return err
		}
		checked, mismatches = int(progress+templates), len(stampRows)+len(couponRows)
		return q.FinishLoyaltyReconciliationRun(ctx, sqlc.FinishLoyaltyReconciliationRunParams{
			ID: runID, FinishedAt: ts(s.clock.Now()), ProgressChecked: int32(progress), TemplatesChecked: int32(templates),
			Mismatches: int32(mismatches), Details: raw,
		})
	})
	if err != nil {
		return 0, 0, fmt.Errorf("stamp: loyalty reconciliation: %w", err)
	}
	if mismatches > 0 {
		s.log.ErrorContext(ctx, "loyalty reconciliation found mismatches", "checked", checked, "mismatches", mismatches)
	} else {
		s.log.InfoContext(ctx, "loyalty reconciliation clean", "checked", checked)
	}
	return checked, mismatches, nil
}
