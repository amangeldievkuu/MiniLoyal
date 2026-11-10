package httpserver

import (
	"context"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/stamp"
)

// Handlers for stamp cards: the member's progress, staff stamping at the
// counter, dashboard cards, progress and adjustments.

func (s *Server) stampService() (*stamp.Service, error) {
	if s.stamps == nil {
		return nil, apierr.Unavailable("stamp cards are not configured")
	}
	return s.stamps, nil
}

func (s *Server) ListMyStamps(ctx context.Context, _ api.ListMyStampsRequestObject) (api.ListMyStampsResponseObject, error) {
	svc, err := s.stampService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	list, err := svc.ListMine(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.ListMyStamps200JSONResponse(toStampProgressList(list)), nil
}

func (s *Server) StaffAddStamps(ctx context.Context, req api.StaffAddStampsRequestObject) (api.StaffAddStampsResponseObject, error) {
	svc, err := s.stampService()
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
	res, err := svc.StaffStamp(s.auditContext(ctx), p, writeIdem(ctx, req.Params.IdempotencyKey, body), stamp.StaffStampInput{
		ScanToken: body.ScanToken, StampCardID: uuid.UUID(body.StampCardId), Count: body.Count,
	})
	if err != nil {
		return nil, err
	}
	return api.StaffAddStamps200JSONResponse(toStampResult(res)), nil
}

func (s *Server) ListStampCards(ctx context.Context, req api.ListStampCardsRequestObject) (api.ListStampCardsResponseObject, error) {
	svc, err := s.stampService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := svc.ListCards(ctx, p, uuid.UUID(req.AppId), strPtr(req.Params.Status))
	if err != nil {
		return nil, err
	}
	out := make(api.ListStampCards200JSONResponse, 0, len(list))
	for _, c := range list {
		out = append(out, toStampCard(c))
	}
	return out, nil
}

func (s *Server) CreateStampCard(ctx context.Context, req api.CreateStampCardRequestObject) (api.CreateStampCardResponseObject, error) {
	svc, err := s.stampService()
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
	in := stamp.CardInput{Name: body.Name, StampsRequired: body.StampsRequired, MaxStampsPerVisit: body.MaxStampsPerVisit}
	if body.RewardCouponTemplateId != nil {
		id := uuid.UUID(*body.RewardCouponTemplateId)
		in.RewardCouponTemplateID = &id
	}
	c, err := svc.CreateCard(s.auditContext(ctx), p, uuid.UUID(req.AppId), writeIdem(ctx, req.Params.IdempotencyKey, body), in)
	if err != nil {
		return nil, err
	}
	return api.CreateStampCard201JSONResponse(toStampCard(c)), nil
}

func (s *Server) GetStampCard(ctx context.Context, req api.GetStampCardRequestObject) (api.GetStampCardResponseObject, error) {
	svc, err := s.stampService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := svc.GetCard(ctx, p, uuid.UUID(req.StampCardId))
	if err != nil {
		return nil, err
	}
	return api.GetStampCard200JSONResponse(toStampCard(c)), nil
}

func (s *Server) UpdateStampCard(ctx context.Context, req api.UpdateStampCardRequestObject) (api.UpdateStampCardResponseObject, error) {
	svc, err := s.stampService()
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
	in := stamp.CardUpdate{
		Name: body.Name, StampsRequired: body.StampsRequired, MaxStampsPerVisit: body.MaxStampsPerVisit, Status: strPtr(body.Status),
	}
	if body.RewardCouponTemplateId != nil {
		id := uuid.UUID(*body.RewardCouponTemplateId)
		in.RewardCouponTemplateID = &id
	}
	if body.Clear != nil {
		for _, c := range *body.Clear {
			if c != api.StampCardClearable("reward_coupon_template_id") {
				return nil, apierr.Validation(map[string]string{"clear": "can clear only reward_coupon_template_id"})
			}
			in.ClearReward = true
		}
	}
	c, err := svc.UpdateCard(s.auditContext(ctx), p, uuid.UUID(req.StampCardId), writeIdem(ctx, req.Params.IdempotencyKey, body), in)
	if err != nil {
		return nil, err
	}
	return api.UpdateStampCard200JSONResponse(toStampCard(c)), nil
}

func (s *Server) ListMemberStamps(ctx context.Context, req api.ListMemberStampsRequestObject) (api.ListMemberStampsResponseObject, error) {
	svc, err := s.stampService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := svc.ListMember(ctx, p, uuid.UUID(req.MemberId))
	if err != nil {
		return nil, err
	}
	return api.ListMemberStamps200JSONResponse(toStampProgressList(list)), nil
}

func (s *Server) AdjustMemberStamps(ctx context.Context, req api.AdjustMemberStampsRequestObject) (api.AdjustMemberStampsResponseObject, error) {
	svc, err := s.stampService()
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
	res, err := svc.Adjust(s.auditContext(ctx), p, uuid.UUID(req.MemberId), writeIdem(ctx, req.Params.IdempotencyKey, body),
		uuid.UUID(body.StampCardId), body.Delta, body.Note)
	if err != nil {
		return nil, err
	}
	return api.AdjustMemberStamps200JSONResponse(toStampResult(res)), nil
}

// ---- mapping

func toStampCard(c stamp.Card) api.StampCard {
	out := api.StampCard{
		Id: openapi_types.UUID(c.ID), AppId: openapi_types.UUID(c.AppID), Name: c.Name,
		StampsRequired: c.StampsRequired, MaxStampsPerVisit: c.MaxStampsPerVisit,
		Status: api.ArchiveStatus(c.Status), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if c.RewardCouponTemplateID != nil {
		id := openapi_types.UUID(*c.RewardCouponTemplateID)
		out.RewardCouponTemplateId = &id
	}
	return out
}

func toStampProgress(p stamp.Progress) api.StampProgress {
	out := api.StampProgress{
		StampCardId: openapi_types.UUID(p.StampCardID), Name: p.Name, StampsRequired: p.StampsRequired,
		MaxStampsPerVisit: p.MaxStampsPerVisit, Status: api.ArchiveStatus(p.Status), Count: p.Count,
		Completions: p.Completions, UnrewardedCompletions: p.UnrewardedCompletions, UpdatedAt: p.UpdatedAt,
	}
	if p.Reward != nil {
		r := toCouponSummary(*p.Reward)
		out.Reward = &r
	}
	return out
}

func toStampProgressList(list []stamp.Progress) []api.StampProgress {
	out := make([]api.StampProgress, 0, len(list))
	for _, p := range list {
		out = append(out, toStampProgress(p))
	}
	return out
}

func toStampResult(r stamp.Result) api.StampResult {
	return api.StampResult{
		Progress: toStampProgress(r.Progress), Added: r.Added, Completed: r.Completed,
		Rewards: toCouponInstances(r.Rewards), RewardsUnavailable: r.RewardsUnavailable,
	}
}

func toStampEvent(e stamp.Event) api.StampEvent {
	out := api.StampEvent{
		Id: e.ID, StampCardId: openapi_types.UUID(e.StampCardID), Delta: e.Delta,
		Reason: api.StampEventReason(e.Reason), CountAfter: e.CountAfter, CreatedAt: e.CreatedAt,
	}
	if e.StoreID != nil {
		id := openapi_types.UUID(*e.StoreID)
		out.StoreId = &id
	}
	return out
}
