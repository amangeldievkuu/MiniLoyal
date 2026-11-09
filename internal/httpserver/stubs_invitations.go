package httpserver

// Phase 2 stubs: team invitations and security events (auth fork).
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) AcceptInvitation(context.Context, api.AcceptInvitationRequestObject) (api.AcceptInvitationResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) RevokeInvitation(context.Context, api.RevokeInvitationRequestObject) (api.RevokeInvitationResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListSecurityEvents(context.Context, api.ListSecurityEventsRequestObject) (api.ListSecurityEventsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListInvitations(context.Context, api.ListInvitationsRequestObject) (api.ListInvitationsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) CreateInvitation(context.Context, api.CreateInvitationRequestObject) (api.CreateInvitationResponseObject, error) {
	return nil, errNotImplemented
}
