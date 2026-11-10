package httpserver

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/points"
)

// Handlers for points: earn rules, balances and history for members, the
// dashboard and the server API; staff earn and redeem at the till; dashboard
// adjustments.

func (s *Server) pointsService() (*points.Service, error) {
	if s.points == nil {
		return nil, apierr.Unavailable("points are not configured")
	}
	return s.points, nil
}

// pointsIdem fingerprints a points write: Idempotency-Key plus method, path
// and body.
func pointsIdem(ctx context.Context, key api.IdempotencyKey, body any) points.Idem {
	method, path := "", ""
	if r := httpRequest(ctx); r != nil {
		method, path = r.Method, r.URL.Path
	}
	raw, _ := json.Marshal(body)
	return points.Idem{Key: string(key), Hash: idempotency.Hash(method, path, raw)}
}

func historyArgs(limit *api.Limit, cursor *api.Cursor) (int, string) {
	l, c := 0, ""
	if limit != nil {
		l = int(*limit)
	}
	if cursor != nil {
		c = string(*cursor)
	}
	return l, c
}

// ---- dashboard: rules

func (s *Server) GetPointsRules(ctx context.Context, req api.GetPointsRulesRequestObject) (api.GetPointsRulesResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	r, err := svc.GetRules(ctx, p, uuid.UUID(req.AppId))
	if err != nil {
		return nil, err
	}
	return api.GetPointsRules200JSONResponse(toPointsRules(r)), nil
}

func (s *Server) PutPointsRules(ctx context.Context, req api.PutPointsRulesRequestObject) (api.PutPointsRulesResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	in := points.Rules{
		Currency: body.Currency, SpendPerPoint: body.SpendPerPoint, PointsPerUnit: body.PointsPerUnit,
		MinSpend: body.MinSpend, MaxPointsPerTxn: body.MaxPointsPerTxn, ExpiryDays: body.ExpiryDays, Enabled: body.Enabled,
	}
	r, err := svc.PutRules(s.auditContext(ctx), p, uuid.UUID(req.AppId), pointsIdem(ctx, req.Params.IdempotencyKey, body), in)
	if err != nil {
		return nil, err
	}
	return api.PutPointsRules200JSONResponse(toPointsRules(r)), nil
}

// ---- dashboard: member points

func (s *Server) GetMemberPoints(ctx context.Context, req api.GetMemberPointsRequestObject) (api.GetMemberPointsResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	sum, err := svc.Summary(ctx, p, uuid.UUID(req.MemberId))
	if err != nil {
		return nil, err
	}
	return api.GetMemberPoints200JSONResponse(toPointsSummary(sum)), nil
}

func (s *Server) ListMemberPointsHistory(ctx context.Context, req api.ListMemberPointsHistoryRequestObject) (api.ListMemberPointsHistoryResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := historyArgs(req.Params.Limit, req.Params.Cursor)
	page, err := svc.History(ctx, p, uuid.UUID(req.MemberId), limit, cursor)
	if err != nil {
		return nil, err
	}
	return api.ListMemberPointsHistory200JSONResponse(toPointsPage(page)), nil
}

func (s *Server) AdjustMemberPoints(ctx context.Context, req api.AdjustMemberPointsRequestObject) (api.AdjustMemberPointsResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	t, err := svc.Adjust(s.auditContext(ctx), p, uuid.UUID(req.MemberId), pointsIdem(ctx, req.Params.IdempotencyKey, body), body.Delta, body.Note)
	if err != nil {
		return nil, err
	}
	return api.AdjustMemberPoints200JSONResponse(toPointsTxn(t)), nil
}

// ---- member

func (s *Server) GetMyPoints(ctx context.Context, _ api.GetMyPointsRequestObject) (api.GetMyPointsResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	sum, err := svc.Summary(ctx, p, p.MemberID)
	if err != nil {
		return nil, err
	}
	return api.GetMyPoints200JSONResponse(toPointsSummary(sum)), nil
}

func (s *Server) ListMyPointsHistory(ctx context.Context, req api.ListMyPointsHistoryRequestObject) (api.ListMyPointsHistoryResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := historyArgs(req.Params.Limit, req.Params.Cursor)
	page, err := svc.History(ctx, p, p.MemberID, limit, cursor)
	if err != nil {
		return nil, err
	}
	return api.ListMyPointsHistory200JSONResponse(toPointsPage(page)), nil
}

// ---- staff app

func (s *Server) StaffEarnPoints(ctx context.Context, req api.StaffEarnPointsRequestObject) (api.StaffEarnPointsResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireStaff(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	t, err := svc.StaffEarn(s.auditContext(ctx), p, pointsIdem(ctx, req.Params.IdempotencyKey, body), points.StaffEarnInput{
		ScanToken: body.ScanToken, Spend: body.SpendAmount, Points: body.Points, Note: body.Note,
	})
	if err != nil {
		return nil, err
	}
	return api.StaffEarnPoints200JSONResponse(toPointsTxn(t)), nil
}

func (s *Server) StaffRedeemPoints(ctx context.Context, req api.StaffRedeemPointsRequestObject) (api.StaffRedeemPointsResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireStaff(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	t, err := svc.StaffRedeem(s.auditContext(ctx), p, pointsIdem(ctx, req.Params.IdempotencyKey, body), body.ScanToken, body.Points, body.Note)
	if err != nil {
		return nil, err
	}
	return api.StaffRedeemPoints200JSONResponse(toPointsTxn(t)), nil
}

// ---- server API

func (s *Server) ServerGetMemberPoints(ctx context.Context, req api.ServerGetMemberPointsRequestObject) (api.ServerGetMemberPointsResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireAPIKey(ctx, "members:read")
	if err != nil {
		return nil, err
	}
	sum, err := svc.Summary(ctx, p, uuid.UUID(req.MemberId))
	if err != nil {
		return nil, err
	}
	return api.ServerGetMemberPoints200JSONResponse(toPointsSummary(sum)), nil
}

func (s *Server) ServerPostPoints(ctx context.Context, req api.ServerPostPointsRequestObject) (api.ServerPostPointsResponseObject, error) {
	svc, err := s.pointsService()
	if err != nil {
		return nil, err
	}
	p, err := requireAPIKey(ctx, "points:write")
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	t, err := svc.ServerPost(ctx, p, uuid.UUID(req.MemberId), pointsIdem(ctx, req.Params.IdempotencyKey, body), points.ServerInput{
		Type: string(body.Type), Points: body.Points, Spend: body.SpendAmount,
		RefType: body.RefType, RefID: body.RefId, Note: body.Note,
	})
	if err != nil {
		return nil, err
	}
	return api.ServerPostPoints200JSONResponse(toPointsTxn(t)), nil
}

// ---- mapping

func toPointsRules(r points.Rules) api.PointsRules {
	return api.PointsRules{
		Currency: r.Currency, SpendPerPoint: r.SpendPerPoint, PointsPerUnit: r.PointsPerUnit,
		MinSpend: r.MinSpend, MaxPointsPerTxn: r.MaxPointsPerTxn, ExpiryDays: r.ExpiryDays,
		Enabled: r.Enabled, UpdatedAt: r.UpdatedAt,
	}
}

func toPointsSummary(sum points.Summary) api.PointsSummary {
	out := api.PointsSummary{
		MemberId:       sum.MemberID,
		Balance:        sum.Balance,
		LifetimeEarned: sum.LifetimeEarned,
		Expiring:       make([]api.PointsExpiring, 0, len(sum.Expiring)),
		Rules: api.PointsRulesPublic{
			Currency: sum.Rules.Currency, SpendPerPoint: sum.Rules.SpendPerPoint, PointsPerUnit: sum.Rules.PointsPerUnit,
			ExpiryDays: sum.Rules.ExpiryDays, Enabled: sum.Rules.Enabled,
		},
	}
	for _, e := range sum.Expiring {
		out.Expiring = append(out.Expiring, api.PointsExpiring{Points: e.Points, ExpiresAt: e.ExpiresAt})
	}
	return out
}

func toPointsTxn(t points.Txn) api.PointsTransaction {
	out := api.PointsTransaction{
		Id: t.ID, MemberId: t.MemberID, Kind: api.PointsKind(t.Kind), Points: t.Points, BalanceAfter: t.BalanceAfter,
		SpendAmount: t.SpendAmount, RefType: t.RefType, RefId: t.RefID, Note: t.Note,
		ActorType: api.PointsTransactionActorType(t.ActorType), CreatedAt: t.CreatedAt,
	}
	if t.StoreID != nil {
		id := *t.StoreID
		out.StoreId = &id
	}
	return out
}

func toPointsPage(p points.Page) api.PointsHistoryPage {
	out := api.PointsHistoryPage{Items: make([]api.PointsTransaction, 0, len(p.Items))}
	for _, t := range p.Items {
		out.Items = append(out.Items, toPointsTxn(t))
	}
	if p.NextCursor != "" {
		c := p.NextCursor
		out.NextCursor = &c
	}
	return out
}
