package points

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// verifyScan checks the scan token and that it was issued to this staff
// member of this app.
func (s *Service) verifyScan(p principal.Principal, token string) (card.ScanClaims, error) {
	if !p.IsStaff() {
		return card.ScanClaims{}, apierr.Unauthorized("sign in to the staff app")
	}
	if s.card == nil {
		return card.ScanClaims{}, apierr.Unavailable("member cards are not configured")
	}
	cl, err := s.card.VerifyScanToken(token)
	if err != nil {
		return card.ScanClaims{}, errScanInvalid
	}
	if cl.StaffID != p.StaffID || cl.AppID != p.AppID || cl.TenantID != p.TenantID {
		return card.ScanClaims{}, errScanMismatch
	}
	return cl, nil
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
	if fields := validateNote(in.Note, false); len(fields) > 0 {
		return Txn{}, apierr.Validation(fields)
	}
	return s.staffOp(ctx, p, cl, idem, func(rules Rules) (operation, error) {
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
	return s.staffOp(ctx, p, cl, idem, func(Rules) (operation, error) {
		return operation{kind: KindRedeem, delta: -int64(points), note: note}, nil
	})
}

func (s *Service) staffOp(ctx context.Context, p principal.Principal, cl card.ScanClaims, idem Idem, build func(Rules) (operation, error)) (Txn, error) {
	var m sqlc.Member
	var out Txn
	err := s.write(ctx, p.TenantID, scopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) {
			m, err = loadMember(ctx, sqlc.New(tx), cl.MemberID, p.AppID)
			return err
		},
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			q := sqlc.New(tx)
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

// earnPoints turns a spend or a fixed number into points under the rule.
func earnPoints(rules Rules, spend, points *int) (int64, error) {
	if spend != nil {
		return rules.PointsFor(*spend)
	}
	if !rules.Enabled {
		return 0, errDisabled
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
	err = s.write(ctx, acc.tenantID, scopeOf(p), idem,
		func(ctx context.Context, tx pgx.Tx) (err error) { m, err = acc.check(ctx, tx); return err },
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
