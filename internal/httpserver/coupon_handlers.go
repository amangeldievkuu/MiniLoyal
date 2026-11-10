package httpserver

import (
	"context"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/access"
	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/coupon"
)

// Handlers for coupons: the member's wallet, offers and claims; staff
// redeeming at the counter; dashboard templates, issues and voids; server
// API issues and lists. Each decodes, calls internal/coupon and encodes.

func (s *Server) couponService() (*coupon.Service, error) {
	if s.coupons == nil {
		return nil, apierr.Unavailable("coupons are not configured")
	}
	return s.coupons, nil
}

// writeIdem fingerprints a coupon or stamp write: Idempotency-Key plus
// method, path and body, exactly as for points.
func writeIdem(ctx context.Context, key api.IdempotencyKey, body any) access.Idem {
	return pointsIdem(ctx, key, body)
}

func strPtr[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

// ---- member

func (s *Server) ListMyCoupons(ctx context.Context, req api.ListMyCouponsRequestObject) (api.ListMyCouponsResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := historyArgs(req.Params.Limit, req.Params.Cursor)
	page, err := svc.ListMine(ctx, p, strPtr(req.Params.State), limit, cursor)
	if err != nil {
		return nil, err
	}
	return api.ListMyCoupons200JSONResponse(toCouponPage(page)), nil
}

func (s *Server) ListCouponOffers(ctx context.Context, _ api.ListCouponOffersRequestObject) (api.ListCouponOffersResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	offers, err := svc.Offers(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make(api.ListCouponOffers200JSONResponse, 0, len(offers))
	for _, o := range offers {
		out = append(out, api.CouponOffer{
			Template: toCouponSummary(o.Template), ValidTo: o.ValidTo, ValidityDays: o.ValidityDays,
			PerMemberLimit: o.PerMemberLimit, Held: o.Held, CanClaim: o.CanClaim,
		})
	}
	return out, nil
}

func (s *Server) ClaimCoupon(ctx context.Context, req api.ClaimCouponRequestObject) (api.ClaimCouponResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	inst, err := svc.Claim(ctx, p, uuid.UUID(req.TemplateId), writeIdem(ctx, req.Params.IdempotencyKey, nil))
	if err != nil {
		return nil, err
	}
	return api.ClaimCoupon201JSONResponse(toCouponInstance(inst)), nil
}

// ---- staff app

func (s *Server) StaffRedeemCoupon(ctx context.Context, req api.StaffRedeemCouponRequestObject) (api.StaffRedeemCouponResponseObject, error) {
	svc, err := s.couponService()
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
	inst, err := svc.Redeem(s.auditContext(ctx), p, uuid.UUID(req.InstanceId), writeIdem(ctx, req.Params.IdempotencyKey, body), body.ScanToken)
	if err != nil {
		return nil, err
	}
	return api.StaffRedeemCoupon200JSONResponse(toCouponInstance(inst)), nil
}

// ---- dashboard: templates

func (s *Server) ListCouponTemplates(ctx context.Context, req api.ListCouponTemplatesRequestObject) (api.ListCouponTemplatesResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := svc.ListTemplates(ctx, p, uuid.UUID(req.AppId), strPtr(req.Params.Status))
	if err != nil {
		return nil, err
	}
	out := make(api.ListCouponTemplates200JSONResponse, 0, len(list))
	for _, t := range list {
		out = append(out, toCouponTemplate(t))
	}
	return out, nil
}

func (s *Server) CreateCouponTemplate(ctx context.Context, req api.CreateCouponTemplateRequestObject) (api.CreateCouponTemplateResponseObject, error) {
	svc, err := s.couponService()
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
	t, err := svc.CreateTemplate(s.auditContext(ctx), p, uuid.UUID(req.AppId), writeIdem(ctx, req.Params.IdempotencyKey, body), coupon.TemplateInput{
		Name: body.Name, Description: body.Description, Type: string(body.Type), Value: body.Value,
		TotalLimit: body.TotalLimit, PerMemberLimit: body.PerMemberLimit, ValidFrom: body.ValidFrom,
		ValidTo: body.ValidTo, ValidityDays: body.ValidityDays, Claimable: body.Claimable,
	})
	if err != nil {
		return nil, err
	}
	return api.CreateCouponTemplate201JSONResponse(toCouponTemplate(t)), nil
}

func (s *Server) GetCouponTemplate(ctx context.Context, req api.GetCouponTemplateRequestObject) (api.GetCouponTemplateResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := svc.GetTemplate(ctx, p, uuid.UUID(req.TemplateId))
	if err != nil {
		return nil, err
	}
	return api.GetCouponTemplate200JSONResponse(toCouponTemplate(t)), nil
}

func (s *Server) UpdateCouponTemplate(ctx context.Context, req api.UpdateCouponTemplateRequestObject) (api.UpdateCouponTemplateResponseObject, error) {
	svc, err := s.couponService()
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
	in := coupon.TemplateUpdate{
		Name: body.Name, Description: body.Description, Type: strPtr(body.Type), Value: body.Value,
		TotalLimit: body.TotalLimit, PerMemberLimit: body.PerMemberLimit, ValidFrom: body.ValidFrom,
		ValidTo: body.ValidTo, ValidityDays: body.ValidityDays, Claimable: body.Claimable, Status: strPtr(body.Status),
	}
	if body.Clear != nil {
		for _, c := range *body.Clear {
			in.Clear = append(in.Clear, string(c))
		}
	}
	t, err := svc.UpdateTemplate(s.auditContext(ctx), p, uuid.UUID(req.TemplateId), writeIdem(ctx, req.Params.IdempotencyKey, body), in)
	if err != nil {
		return nil, err
	}
	return api.UpdateCouponTemplate200JSONResponse(toCouponTemplate(t)), nil
}

// ---- dashboard: member coupons

func (s *Server) ListMemberCoupons(ctx context.Context, req api.ListMemberCouponsRequestObject) (api.ListMemberCouponsResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := historyArgs(req.Params.Limit, req.Params.Cursor)
	page, err := svc.ListMember(ctx, p, uuid.UUID(req.MemberId), strPtr(req.Params.State), limit, cursor)
	if err != nil {
		return nil, err
	}
	return api.ListMemberCoupons200JSONResponse(toCouponPage(page)), nil
}

func (s *Server) IssueMemberCoupon(ctx context.Context, req api.IssueMemberCouponRequestObject) (api.IssueMemberCouponResponseObject, error) {
	svc, err := s.couponService()
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
	inst, err := svc.Issue(s.auditContext(ctx), p, uuid.UUID(req.MemberId), uuid.UUID(body.TemplateId), writeIdem(ctx, req.Params.IdempotencyKey, body))
	if err != nil {
		return nil, err
	}
	return api.IssueMemberCoupon201JSONResponse(toCouponInstance(inst)), nil
}

func (s *Server) VoidCoupon(ctx context.Context, req api.VoidCouponRequestObject) (api.VoidCouponResponseObject, error) {
	svc, err := s.couponService()
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
	inst, err := svc.Void(s.auditContext(ctx), p, uuid.UUID(req.InstanceId), writeIdem(ctx, req.Params.IdempotencyKey, body), body.Reason)
	if err != nil {
		return nil, err
	}
	return api.VoidCoupon200JSONResponse(toCouponInstance(inst)), nil
}

// ---- server API

func (s *Server) ServerListMemberCoupons(ctx context.Context, req api.ServerListMemberCouponsRequestObject) (api.ServerListMemberCouponsResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireAPIKey(ctx, "members:read")
	if err != nil {
		return nil, err
	}
	limit, cursor := historyArgs(req.Params.Limit, req.Params.Cursor)
	page, err := svc.ListMember(ctx, p, uuid.UUID(req.MemberId), strPtr(req.Params.State), limit, cursor)
	if err != nil {
		return nil, err
	}
	return api.ServerListMemberCoupons200JSONResponse(toCouponPage(page)), nil
}

func (s *Server) ServerIssueCoupon(ctx context.Context, req api.ServerIssueCouponRequestObject) (api.ServerIssueCouponResponseObject, error) {
	svc, err := s.couponService()
	if err != nil {
		return nil, err
	}
	p, err := requireAPIKey(ctx, "coupons:write")
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	inst, err := svc.Issue(ctx, p, uuid.UUID(req.MemberId), uuid.UUID(body.TemplateId), writeIdem(ctx, req.Params.IdempotencyKey, body))
	if err != nil {
		return nil, err
	}
	return api.ServerIssueCoupon201JSONResponse(toCouponInstance(inst)), nil
}

// ---- mapping

func toCouponSummary(t coupon.Summary) api.CouponTemplateSummary {
	return api.CouponTemplateSummary{
		Id: openapi_types.UUID(t.ID), Name: t.Name, Description: t.Description, Type: api.CouponType(t.Type), Value: t.Value,
	}
}

func toCouponInstance(i coupon.Instance) api.CouponInstance {
	out := api.CouponInstance{
		Id: openapi_types.UUID(i.ID), Code: i.Code, Template: toCouponSummary(i.Template),
		MemberId: openapi_types.UUID(i.MemberID), Status: api.CouponInstanceStatus(i.Status),
		State: api.CouponState(i.State), Source: api.CouponSource(i.Source), IssuedAt: i.IssuedAt,
		ExpiresAt: i.ExpiresAt, RedeemedAt: i.RedeemedAt,
	}
	if i.Store != nil {
		out.Store = &api.StoreRef{Id: openapi_types.UUID(i.Store.ID), Name: i.Store.Name}
	}
	return out
}

func toCouponInstances(items []coupon.Instance) []api.CouponInstance {
	out := make([]api.CouponInstance, 0, len(items))
	for _, i := range items {
		out = append(out, toCouponInstance(i))
	}
	return out
}

func toCouponPage(p coupon.Page) api.CouponInstancePage {
	out := api.CouponInstancePage{Items: toCouponInstances(p.Items)}
	if p.NextCursor != "" {
		c := p.NextCursor
		out.NextCursor = &c
	}
	return out
}

func toCouponTemplate(t coupon.Template) api.CouponTemplate {
	return api.CouponTemplate{
		Id: openapi_types.UUID(t.ID), AppId: openapi_types.UUID(t.AppID), Name: t.Name, Description: t.Description,
		Type: api.CouponType(t.Type), Value: t.Value, TotalLimit: t.TotalLimit, PerMemberLimit: t.PerMemberLimit,
		ValidFrom: t.ValidFrom, ValidTo: t.ValidTo, ValidityDays: t.ValidityDays, Claimable: t.Claimable,
		ClaimedCount: t.ClaimedCount, Status: api.ArchiveStatus(t.Status), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}
