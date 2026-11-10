package httpserver

// Phase 3 stubs: points rules, balances, history, adjustments, staff earn and redeem, server points (points fork).
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) GetPointsRules(context.Context, api.GetPointsRulesRequestObject) (api.GetPointsRulesResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) PutPointsRules(context.Context, api.PutPointsRulesRequestObject) (api.PutPointsRulesResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetMemberPoints(context.Context, api.GetMemberPointsRequestObject) (api.GetMemberPointsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) AdjustMemberPoints(context.Context, api.AdjustMemberPointsRequestObject) (api.AdjustMemberPointsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListMemberPointsHistory(context.Context, api.ListMemberPointsHistoryRequestObject) (api.ListMemberPointsHistoryResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetMyPoints(context.Context, api.GetMyPointsRequestObject) (api.GetMyPointsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListMyPointsHistory(context.Context, api.ListMyPointsHistoryRequestObject) (api.ListMyPointsHistoryResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ServerGetMemberPoints(context.Context, api.ServerGetMemberPointsRequestObject) (api.ServerGetMemberPointsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ServerPostPoints(context.Context, api.ServerPostPointsRequestObject) (api.ServerPostPointsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) StaffEarnPoints(context.Context, api.StaffEarnPointsRequestObject) (api.StaffEarnPointsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) StaffRedeemPoints(context.Context, api.StaffRedeemPointsRequestObject) (api.StaffRedeemPointsResponseObject, error) {
	return nil, errNotImplemented
}
