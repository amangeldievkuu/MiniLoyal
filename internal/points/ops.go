package points

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// StaffEarnInput is a purchase recorded at the till. Exactly one of Spend
// (the rule applies) or Points (managers only) is set.
type StaffEarnInput struct {
	ScanToken string
	Spend     *int
	Points    *int
	Note      *string
}

// verifyScan checks the scan token's MAC and that it was issued to this
// staff member of this app (access.VerifyScan).
func (s *Service) verifyScan(p principal.Principal, token string) (card.ScanClaims, error) {
	return access.VerifyScan(s.card, p, token)
}

// StaffEarn gives points for a purchase to the member whose card was scanned.
func (s *Service) StaffEarn(ctx context.Context, p principal.Principal, idem Idem, in StaffEarnInput) (Txn, error) {
	cl, err := s.verifyScan(p, in.ScanToken)
	if err != nil {
		return Txn{}, err
	}
	if (in.Spend == nil) == (in.Points == nil) {
		return Txn{}, apierr.Validation(map[string]string{"spend_amount": "send either spend_amount or points"})
	}
	if in.Points != nil && !p.IsManager() {
		return Txn{}, apierr.Forbidden("only managers can give a fixed number of points")
	}
	if in.Points != nil && (*in.Points < 1 || *in.Points > MaxPointsPerOperation) {
		return Txn{}, apierr.Validation(map[string]string{"points": "must be 1 to 1000000"})
	}
	if in.Spend != nil && (*in.Spend < 0 || *in.Spend > MaxSpendAmount) {
		return Txn{}, apierr.Validation(map[string]string{"spend_amount": fmt.Sprintf("must be 0 to %d", MaxSpendAmount)})
	}
	if fields := validateNote(in.Note, false); len(fields) > 0 {
		return Txn{}, apierr.Validation(fields)
	}
	return s.staffOp(ctx, p, cl, idem, false, func(rules Rules) (operation, error) {
		pts, err := earnPoints(rules, in.Spend, in.Points)
		if err != nil {
			return operation{}, err
		}
		return operation{kind: KindEarn, delta: pts, expires: true, earnedDelta: pts, spend: in.Spend, note: in.Note}, nil
	})
}

// StaffRedeem spends points of the member whose card was scanned.
func (s *Service) StaffRedeem(ctx context.Context, p principal.Principal, idem Idem, scanToken string, points int, note *string) (Txn, error) {
	cl, err := s.verifyScan(p, scanToken)
	if err != nil {
		return Txn{}, err
	}
	if points < 1 || points > MaxPointsPerOperation {
		return Txn{}, apierr.Validation(map[string]string{"points": "must be 1 to 1000000"})
	}
	if fields := validateNote(note, false); len(fields) > 0 {
		return Txn{}, apierr.Validation(fields)
	}
	return s.staffOp(ctx, p, cl, idem, true, func(Rules) (operation, error) {
		return operation{kind: KindRedeem, delta: -int64(points), note: note}, nil
	})
}

// staffOp runs a staff points write. Authorization (member in the app, not
// the staff member's own card) runs before the idempotency claim, so replays
// are authorized too. The scan's lifetime, the card key version and the
// single-use rule for redemptions are checked only for a first write; a
// retry with the same Idempotency-Key replays the stored outcome instead.
func (s *Service) staffOp(ctx context.Context, p principal.Principal, cl card.ScanClaims, idem Idem, singleUse bool, build func(Rules) (operation, error)) (Txn, error) {
	if err := access.AllowStaff(s.staffLimit, p); err != nil {
		return Txn{}, err
	}
	var m sqlc.Member
	var out Txn
	err := s.write(ctx, p.TenantID, scopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) {
			m, err = access.StaffMember(ctx, sqlc.New(tx), p, cl, errSelfService)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
			now := s.clock.Now()
			if err := access.ScanFresh(cl, m, now); err != nil {
				return nil, err
			}
			if singleUse {
				n, err := q.UseScanToken(ctx, sqlc.UseScanTokenParams{
					Jti: cl.JTI, TenantID: p.TenantID, AppID: p.AppID, UsedAt: pgtype.Timestamptz{Time: now, Valid: true},
				})
				if err != nil {
					return nil, fmt.Errorf("points: use scan: %w", err)
				}
				if n == 0 {
					return nil, errScanUsed
				}
			}
			rules, err := rulesFor(ctx, q, m.AppID)
			if err != nil {
				return nil, err
			}
			op, err := build(rules)
			if err != nil {
				return nil, err
			}
			op.actorType, op.actorID = actorOf(p)
			op.storeID = p.StoreID
			return s.apply(ctx, q, m, rules, op)
		}, &out)
	return out, err
}

// earnPoints turns a spend or a fixed number into points under the rule. A
// fixed number above the rule's cap (or the platform ceiling) is refused.
func earnPoints(rules Rules, spend, points *int) (int64, error) {
	if spend != nil {
		return rules.PointsFor(*spend)
	}
	if !rules.Enabled {
		return 0, errDisabled
	}
	if limit := rules.earnCap(); int64(*points) > limit {
		return 0, apierr.Validation(map[string]string{"points": fmt.Sprintf("at most %d points per purchase under this app's rule", limit)})
	}
	return int64(*points), nil
}

// ServerInput is POST /v1/members/{id}/points from a POS or brand backend.
type ServerInput struct {
	Type    string // earn, redeem or adjust
	Points  *int
	Spend   *int
	RefType *string
	RefID   *string
	Note    *string
}

// ServerPost earns, redeems or adjusts points for an API key with the
// points:write scope, inside the key's own app.
func (s *Service) ServerPost(ctx context.Context, p principal.Principal, memberID uuid.UUID, idem Idem, in ServerInput) (Txn, error) {
	if !p.IsAPIKey() {
		return Txn{}, errNotFound
	}
	fields := validateRef(in.RefType, in.RefID)
	for k, v := range validateNote(in.Note, false) {
		fields[k] = v
	}
	if in.Spend != nil && (*in.Spend < 0 || *in.Spend > MaxSpendAmount) {
		fields["spend_amount"] = fmt.Sprintf("must be 0 to %d", MaxSpendAmount)
	}
	if in.Points != nil && (*in.Points > MaxPointsPerOperation || *in.Points < -MaxPointsPerOperation) {
		fields["points"] = fmt.Sprintf("must be at most %d either way", MaxPointsPerOperation)
	}
	switch in.Type {
	case KindEarn:
		if (in.Spend == nil) == (in.Points == nil) {
			fields["spend_amount"] = "send either spend_amount or points"
		} else if in.Points != nil && *in.Points < 1 {
			fields["points"] = "must be positive to earn"
		}
	case KindRedeem:
		if in.Spend != nil {
			fields["spend_amount"] = "only applies to earn"
		}
		if in.Points == nil || *in.Points < 1 {
			fields["points"] = "must be positive to redeem"
		}
	case KindAdjust:
		if in.Spend != nil {
			fields["spend_amount"] = "only applies to earn"
		}
		if in.Points == nil || *in.Points == 0 {
			fields["points"] = "must be non-zero to adjust"
		}
	default:
		fields["type"] = "must be earn, redeem or adjust"
	}
	if len(fields) > 0 {
		return Txn{}, apierr.Validation(fields)
	}
	acc, err := s.reach(ctx, p, memberID, principal.RoleAdmin)
	if err != nil {
		return Txn{}, err
	}
	var m sqlc.Member
	var out Txn
	err = s.write(ctx, acc.TenantID, scopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) { m, err = acc.Check(ctx, tx); return err },
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
			rules, err := rulesFor(ctx, q, m.AppID)
			if err != nil {
				return nil, err
			}
			op := operation{kind: in.Type, spend: in.Spend, refType: in.RefType, refID: in.RefID, note: in.Note}
			switch in.Type {
			case KindEarn:
				pts, err := earnPoints(rules, in.Spend, in.Points)
				if err != nil {
					return nil, err
				}
				op.delta, op.expires, op.earnedDelta = pts, true, pts
			case KindRedeem:
				op.delta = -int64(*in.Points)
			case KindAdjust:
				op.delta = int64(*in.Points)
			}
			op.actorType, op.actorID = actorOf(p)
			return s.apply(ctx, q, m, rules, op)
		}, &out)
	return out, err
}
