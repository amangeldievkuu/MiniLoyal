package httpserver

import (
	"context"
	"net/url"
	"strings"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Team invitations and the account security history.

// inviteURL is the link the invitee opens. The dashboard reads the token
// from the query string and posts it to acceptInvitation.
func (s *Server) inviteURL(token string) string {
	return strings.TrimRight(s.cfg.DashboardOrigin, "/") + "/accept-invite?token=" + url.QueryEscape(token)
}

func toInvitation(i tenant.Invitation) api.Invitation {
	return api.Invitation{
		Id:        openapi_types.UUID(i.ID),
		Email:     openapi_types.Email(i.Email),
		Role:      api.Role(i.Role),
		ExpiresAt: i.ExpiresAt,
		CreatedAt: i.CreatedAt,
	}
}

func (s *Server) ListInvitations(ctx context.Context, req api.ListInvitationsRequestObject) (api.ListInvitationsResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.tenants.ListInvitations(ctx, p, uuid.UUID(req.TenantId))
	if err != nil {
		return nil, err
	}
	out := make(api.ListInvitations200JSONResponse, 0, len(items))
	for _, i := range items {
		out = append(out, toInvitation(i))
	}
	return out, nil
}

func (s *Server) CreateInvitation(ctx context.Context, req api.CreateInvitationRequestObject) (api.CreateInvitationResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	created, err := s.tenants.CreateInvitation(ctx, p, uuid.UUID(req.TenantId), idem(ctx, req.Params.IdempotencyKey, body), string(body.Email), principal.Role(body.Role))
	if err != nil {
		return nil, err
	}
	inv := toInvitation(created.Invitation)
	return api.CreateInvitation201JSONResponse{
		Id: inv.Id, Email: inv.Email, Role: inv.Role, ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt,
		InviteUrl: s.inviteURL(created.Token),
	}, nil
}

func (s *Server) RevokeInvitation(ctx context.Context, req api.RevokeInvitationRequestObject) (api.RevokeInvitationResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tenants.RevokeInvitation(ctx, p, uuid.UUID(req.InvitationId)); err != nil {
		return nil, err
	}
	return api.RevokeInvitation204Response{}, nil
}

func (s *Server) AcceptInvitation(ctx context.Context, req api.AcceptInvitationRequestObject) (api.AcceptInvitationResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierr.BadRequest("request body is required")
	}
	sess, err := svc.AcceptInvitation(ctx, req.Body.Token, req.Body.Password, req.Body.DisplayName, s.authClient(ctx))
	if err != nil {
		return nil, err
	}
	s.setRefreshCookie(ctx, sess.RefreshToken)
	return api.AcceptInvitation200JSONResponse(toAPISession(sess)), nil
}

func (s *Server) ListSecurityEvents(ctx context.Context, req api.ListSecurityEventsRequestObject) (api.ListSecurityEventsResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := 0, ""
	if req.Params.Limit != nil {
		limit = int(*req.Params.Limit)
	}
	if req.Params.Cursor != nil {
		cursor = string(*req.Params.Cursor)
	}
	page, err := svc.ListSecurityEvents(ctx, p.UserID, limit, cursor)
	if err != nil {
		return nil, err
	}
	out := api.SecurityEventPage{Items: make([]api.SecurityEvent, 0, len(page.Items))}
	for _, e := range page.Items {
		meta := e.Metadata
		out.Items = append(out.Items, api.SecurityEvent{
			Id: e.ID, Event: e.Event, Ip: e.IP, UserAgent: e.UserAgent, Metadata: &meta, CreatedAt: e.CreatedAt,
		})
	}
	if page.NextCursor != "" {
		c := page.NextCursor
		out.NextCursor = &c
	}
	return api.ListSecurityEvents200JSONResponse(out), nil
}

// withDevice attaches the calling device to ctx for auth methods that take
// no Client parameter, so their security events carry IP and user agent.
func (s *Server) withDevice(ctx context.Context) context.Context {
	return auth.WithClient(ctx, s.authClient(ctx))
}
