package httpserver

// Phase 3 stubs: staff sign-in, staff profile, card scan, member card, card rotation, staff invite reset, member session end (staff fork).
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) EndMemberSessions(context.Context, api.EndMemberSessionsRequestObject) (api.EndMemberSessionsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) RotateMemberCard(context.Context, api.RotateMemberCardRequestObject) (api.RotateMemberCardResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ResetStaffInvite(context.Context, api.ResetStaffInviteRequestObject) (api.ResetStaffInviteResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetMyCard(context.Context, api.GetMyCardRequestObject) (api.GetMyCardResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) StaffLoginWithLine(context.Context, api.StaffLoginWithLineRequestObject) (api.StaffLoginWithLineResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetStaffMe(context.Context, api.GetStaffMeRequestObject) (api.GetStaffMeResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) StaffScan(context.Context, api.StaffScanRequestObject) (api.StaffScanResponseObject, error) {
	return nil, errNotImplemented
}
