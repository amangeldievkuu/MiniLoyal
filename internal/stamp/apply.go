package stamp

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/coupon"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/events"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// operation is one change to a member's stamps on one card.
type operation struct {
	reason    string // stamp or adjust
	delta     int
	scanID    *string // stamps only: the scan token's jti
	note      *string // adjustments only
	staffID   uuid.UUID
	storeID   uuid.UUID
	actorType string
	actorID   uuid.UUID
}

// apply records op inside tx, which must be scoped to m's tenant and hold
// the card (GetStampCardForShare). It locks the member's progress row
// first, so concurrent operations on one member and card serialize and
// each sees the rows the previous one committed.
func (s *Service) apply(ctx context.Context, tx pgx.Tx, m sqlc.Member, c sqlc.StampCard, op operation, now time.Time) (Result, error) {
	q := sqlc.New(tx)
	if err := q.EnsureStampProgress(ctx, sqlc.EnsureStampProgressParams{MemberID: m.ID, StampCardID: c.ID, TenantID: m.TenantID, AppID: m.AppID}); err != nil {
		return Result{}, fmt.Errorf("stamp: ensure progress: %w", err)
	}
	prog, err := q.LockStampProgress(ctx, sqlc.LockStampProgressParams{AppID: m.AppID, MemberID: m.ID, StampCardID: c.ID})
	if err != nil {
		return Result{}, fmt.Errorf("stamp: lock progress: %w", err)
	}
	if op.reason == ReasonStamp {
		// Under the progress lock, so concurrent stamps for this member and
		// card, on one scan or several, see each other's rows.
		given, err := q.SumVisitStamps(ctx, sqlc.SumVisitStampsParams{
			AppID: m.AppID, StampCardID: c.ID, MemberID: m.ID, Since: ts(now.Add(-VisitWindow)),
		})
		if err != nil {
			return Result{}, fmt.Errorf("stamp: stamps this visit: %w", err)
		}
		if int(given)+op.delta > int(c.MaxStampsPerVisit) {
			return Result{}, errVisitLimit.WithDetails(map[string]any{
				"max_stamps_per_visit": int(c.MaxStampsPerVisit), "given_this_visit": int(given),
				"visit_window_minutes": int(VisitWindow / time.Minute),
			})
		}
	}
	count := int(prog.Count) + op.delta
	if count < 0 {
		return Result{}, errInsufficient
	}
	completions := int(prog.Completions)
	txnID := uuid.Must(uuid.NewV7())
	event := func(reason string, delta, countAfter int, couponID uuid.UUID, scanID, note *string) error {
		_, err := q.InsertStampEvent(ctx, sqlc.InsertStampEventParams{
			TenantID: m.TenantID, AppID: m.AppID, MemberID: m.ID, StampCardID: c.ID, TxnID: txnID,
			Delta: int32(delta), CountAfter: int32(countAfter), CompletionsAfter: int32(completions),
			Reason: reason, CouponInstanceID: access.NullUUID(couponID), ScanID: scanID, Note: note,
			StaffID: access.NullUUID(op.staffID), StoreID: access.NullUUID(op.storeID),
			ActorType: op.actorType, ActorID: access.NullUUID(op.actorID), CreatedAt: ts(now),
		})
		if err != nil {
			return fmt.Errorf("stamp: %s event: %w", reason, err)
		}
		return nil
	}
	if err := event(op.reason, op.delta, count, uuid.Nil, op.scanID, op.note); err != nil {
		return Result{}, err
	}
	res := Result{Added: op.delta, Rewards: []coupon.Instance{}}
	required := int(c.StampsRequired)
	for count >= required {
		count -= required
		completions++
		var couponID uuid.UUID
		var note *string
		if c.RewardCouponTemplateID.Valid {
			inst, ok, err := coupon.IssueReward(ctx, tx, m, c.RewardCouponTemplateID.UUID, op.actorType, op.actorID, txnID, now)
			if err != nil {
				return Result{}, err
			}
			if ok {
				couponID = inst.ID
				res.Rewards = append(res.Rewards, inst)
			} else {
				unavailable := NoteRewardUnavailable
				note = &unavailable
				res.RewardsUnavailable++
			}
		}
		if err := event(ReasonCompletion, -required, count, couponID, op.scanID, note); err != nil {
			return Result{}, err
		}
		payload := map[string]any{
			"stamp_card_id": c.ID, "member_id": m.ID, "completions": completions, "txn_id": txnID,
			"reward_unavailable": note != nil,
		}
		if couponID != uuid.Nil {
			payload["coupon_id"] = couponID
		}
		if err := events.Emit(ctx, q, events.Event{
			TenantID: m.TenantID, AppID: m.AppID, Type: events.StampCompleted, MemberID: m.ID, OccurredAt: now, Payload: payload,
		}); err != nil {
			return Result{}, err
		}
		res.Completed++
	}
	set := sqlc.SetStampProgressParams{
		Count: int32(count), Completions: int32(completions), Now: ts(now),
		AppID: m.AppID, MemberID: m.ID, StampCardID: c.ID,
	}
	if op.reason == ReasonStamp {
		set.LastStampedAt = ts(now)
	}
	if err := q.SetStampProgress(ctx, set); err != nil {
		return Result{}, fmt.Errorf("stamp: set progress: %w", err)
	}
	res.Progress, err = progressOn(ctx, q, m, c.ID)
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// progressOn reads the member's progress on one card.
func progressOn(ctx context.Context, q *sqlc.Queries, m sqlc.Member, cardID uuid.UUID) (Progress, error) {
	rows, err := q.ListMemberStampProgress(ctx, sqlc.ListMemberStampProgressParams{MemberID: m.ID, AppID: m.AppID, IncludeArchived: true})
	if err != nil {
		return Progress{}, fmt.Errorf("stamp: progress: %w", err)
	}
	for _, r := range rows {
		if r.StampCardID == cardID {
			return toProgress(r), nil
		}
	}
	return Progress{}, fmt.Errorf("stamp: no progress row for card %s", cardID)
}

// cardForShare reads the card in the member's app and holds it against a
// concurrent change of stamps_required until the transaction ends.
func cardForShare(ctx context.Context, q *sqlc.Queries, appID, cardID uuid.UUID) (sqlc.StampCard, error) {
	c, err := q.GetStampCardForShare(ctx, sqlc.GetStampCardForShareParams{AppID: appID, ID: cardID})
	if err != nil {
		if db.IsNotFound(err) {
			return sqlc.StampCard{}, errCardNotFound
		}
		return sqlc.StampCard{}, fmt.Errorf("stamp: load card: %w", err)
	}
	return c, nil
}

// StaffStampInput is a stamp given at the counter.
type StaffStampInput struct {
	ScanToken   string
	StampCardID uuid.UUID
	Count       int
}

// StaffStamp adds stamps to the scanned member's card. The scan works as
// for points: bound to this staff member and app, its lifetime checked
// only on a first write so a retry replays after it ran out, and the
// per-staff limit shared with points and coupons.
func (s *Service) StaffStamp(ctx context.Context, p principal.Principal, idem Idem, in StaffStampInput) (Result, error) {
	cl, err := access.VerifyScan(s.card, p, in.ScanToken)
	if err != nil {
		return Result{}, err
	}
	if in.Count < 1 || in.Count > MaxStampsPerOperation {
		return Result{}, apierr.Validation(map[string]string{"count": fmt.Sprintf("must be 1 to %d", MaxStampsPerOperation)})
	}
	if err := access.AllowStaff(s.staffLimit, p); err != nil {
		return Result{}, err
	}
	var m sqlc.Member
	var out Result
	err = s.writer.Write(ctx, p.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) {
			m, err = access.StaffMember(ctx, sqlc.New(tx), p, cl, errSelfService)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			now := s.clock.Now()
			if err := access.ScanFresh(cl, m, now); err != nil {
				return nil, err
			}
			c, err := cardForShare(ctx, sqlc.New(tx), p.AppID, in.StampCardID)
			if err != nil {
				return nil, err
			}
			if c.Status != "active" {
				return nil, errInactive
			}
			actorType, actorID := access.ActorOf(p)
			scanID := cl.JTI
			res, err := s.apply(ctx, tx, m, c, operation{
				reason: ReasonStamp, delta: in.Count, scanID: &scanID,
				staffID: p.StaffID, storeID: p.StoreID, actorType: actorType, actorID: actorID,
			}, now)
			if err != nil {
				return nil, err
			}
			appID := m.AppID
			meta := map[string]any{
				"staff_id": p.StaffID.String(), "stamp_card_id": c.ID.String(),
				"count": in.Count, "completed": res.Completed,
			}
			if p.StoreID != uuid.Nil {
				meta["store_id"] = p.StoreID.String()
			}
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: m.TenantID, AppID: &appID, Actor: p, Action: ActionAdd,
				TargetType: "member", TargetID: m.ID.String(), Metadata: meta,
			}); err != nil {
				return nil, err
			}
			return res, nil
		}, &out)
	return out, err
}

// Adjust corrects a member's stamps on one card for a dashboard owner or
// admin; audited. The per-visit cap does not apply and archived cards can
// be corrected. A positive delta can complete the card like a stamp; a
// negative one cannot go below zero and never undoes past completions.
func (s *Service) Adjust(ctx context.Context, p principal.Principal, memberID uuid.UUID, idem Idem, cardID uuid.UUID, delta int, note string) (Result, error) {
	if !p.IsUser() {
		return Result{}, access.ErrMemberNotFound
	}
	fields := map[string]string{}
	if delta == 0 || delta < -MaxStampsPerOperation || delta > MaxStampsPerOperation {
		fields["delta"] = fmt.Sprintf("must be -%d to %d and not zero", MaxStampsPerOperation, MaxStampsPerOperation)
	}
	note = strings.TrimSpace(note)
	if n := utf8.RuneCountInString(note); n < 1 || n > 200 || !utf8.ValidString(note) || strings.ContainsRune(note, 0) {
		fields["note"] = "must be 1 to 200 characters of text"
	}
	if len(fields) > 0 {
		return Result{}, apierr.Validation(fields)
	}
	acc, err := access.Reach(ctx, s.pool, s.tenants, p, memberID, principal.RoleAdmin)
	if err != nil {
		return Result{}, err
	}
	var m sqlc.Member
	var out Result
	err = s.writer.Write(ctx, acc.TenantID, access.ScopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) {
			if m, err = acc.Check(ctx, tx); err != nil {
				return err
			}
			// The note is personal text an erasure must blank: serialize with
			// it, and refuse a member erased meanwhile.
			return access.ShareMember(ctx, sqlc.New(tx), m.AppID, m.ID)
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			now := s.clock.Now()
			c, err := cardForShare(ctx, sqlc.New(tx), m.AppID, cardID)
			if err != nil {
				return nil, err
			}
			actorType, actorID := access.ActorOf(p)
			res, err := s.apply(ctx, tx, m, c, operation{
				reason: ReasonAdjust, delta: delta, note: &note, actorType: actorType, actorID: actorID,
			}, now)
			if err != nil {
				return nil, err
			}
			appID := m.AppID
			if err := audit.Record(ctx, tx, audit.Entry{
				TenantID: m.TenantID, AppID: &appID, Actor: p, Action: ActionAdjust,
				TargetType: "member", TargetID: m.ID.String(),
				// No note: audit rows outlive a PDPA erasure, which blanks it.
				Metadata: map[string]any{"stamp_card_id": c.ID.String(), "delta": delta, "completed": res.Completed},
			}); err != nil {
				return nil, err
			}
			return res, nil
		}, &out)
	return out, err
}
