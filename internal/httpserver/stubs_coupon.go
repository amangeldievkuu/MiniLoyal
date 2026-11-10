package httpserver

// Phase 4 stubs: member coupons and offers, claim, staff redeem, dashboard templates, issue and void, server issue and list (coupons module).
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) ListMyCoupons(context.Context, api.ListMyCouponsRequestObject) (api.ListMyCouponsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListCouponOffers(context.Context, api.ListCouponOffersRequestObject) (api.ListCouponOffersResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ClaimCoupon(context.Context, api.ClaimCouponRequestObject) (api.ClaimCouponResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) StaffRedeemCoupon(context.Context, api.StaffRedeemCouponRequestObject) (api.StaffRedeemCouponResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListCouponTemplates(context.Context, api.ListCouponTemplatesRequestObject) (api.ListCouponTemplatesResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) CreateCouponTemplate(context.Context, api.CreateCouponTemplateRequestObject) (api.CreateCouponTemplateResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetCouponTemplate(context.Context, api.GetCouponTemplateRequestObject) (api.GetCouponTemplateResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateCouponTemplate(context.Context, api.UpdateCouponTemplateRequestObject) (api.UpdateCouponTemplateResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListMemberCoupons(context.Context, api.ListMemberCouponsRequestObject) (api.ListMemberCouponsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) IssueMemberCoupon(context.Context, api.IssueMemberCouponRequestObject) (api.IssueMemberCouponResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) VoidCoupon(context.Context, api.VoidCouponRequestObject) (api.VoidCouponResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ServerListMemberCoupons(context.Context, api.ServerListMemberCouponsRequestObject) (api.ServerListMemberCouponsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ServerIssueCoupon(context.Context, api.ServerIssueCouponRequestObject) (api.ServerIssueCouponResponseObject, error) {
	return nil, errNotImplemented
}
