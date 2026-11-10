package httpserver

// Phase 4 stubs: member stamps, staff stamps, dashboard stamp cards, member stamps and adjustments (stamps module).
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) ListMyStamps(context.Context, api.ListMyStampsRequestObject) (api.ListMyStampsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) StaffAddStamps(context.Context, api.StaffAddStampsRequestObject) (api.StaffAddStampsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListStampCards(context.Context, api.ListStampCardsRequestObject) (api.ListStampCardsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) CreateStampCard(context.Context, api.CreateStampCardRequestObject) (api.CreateStampCardResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetStampCard(context.Context, api.GetStampCardRequestObject) (api.GetStampCardResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateStampCard(context.Context, api.UpdateStampCardRequestObject) (api.UpdateStampCardResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListMemberStamps(context.Context, api.ListMemberStampsRequestObject) (api.ListMemberStampsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) AdjustMemberStamps(context.Context, api.AdjustMemberStampsRequestObject) (api.AdjustMemberStampsResponseObject, error) {
	return nil, errNotImplemented
}
