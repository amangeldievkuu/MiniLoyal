package httpserver

// Phase 2 stubs: member sign-in, profile, consent, export, erasure and member lists (lineauth and member forks).
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) LoginWithLine(context.Context, api.LoginWithLineRequestObject) (api.LoginWithLineResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListMembers(context.Context, api.ListMembersRequestObject) (api.ListMembersResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) EraseMember(context.Context, api.EraseMemberRequestObject) (api.EraseMemberResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetMember(context.Context, api.GetMemberRequestObject) (api.GetMemberResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ExportMember(context.Context, api.ExportMemberRequestObject) (api.ExportMemberResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DeleteMe(context.Context, api.DeleteMeRequestObject) (api.DeleteMeResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetMe(context.Context, api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateMe(context.Context, api.UpdateMeRequestObject) (api.UpdateMeResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) RecordConsent(context.Context, api.RecordConsentRequestObject) (api.RecordConsentResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ExportMe(context.Context, api.ExportMeRequestObject) (api.ExportMeResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ServerListMembers(context.Context, api.ServerListMembersRequestObject) (api.ServerListMembersResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ServerGetMember(context.Context, api.ServerGetMemberRequestObject) (api.ServerGetMemberResponseObject, error) {
	return nil, errNotImplemented
}
