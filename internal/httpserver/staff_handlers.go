package httpserver

import (
	"context"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/staffauth"
)

// Handlers for the staff app: sign-in with LINE, the signed-in staff member,
// and dashboard actions on staff (invite reset).

func (s *Server) staffService() (*staffauth.Service, error) {
	if s.staffAuth == nil {
		return nil, apierr.Unavailable("the staff app is not configured")
	}
	return s.staffAuth, nil
}

func (s *Server) StaffLoginWithLine(ctx context.Context, req api.StaffLoginWithLineRequestObject) (api.StaffLoginWithLineResponseObject, error) {
	svc, err := s.staffService()
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	in := staffauth.LoginInput{AppID: uuid.UUID(body.AppId), IDToken: body.IdToken}
	if body.InviteCode != nil {
		in.InviteCode = *body.InviteCode
	}
	c := staffauth.Client{UserAgent: userAgent(ctx)}
	if r := httpRequest(ctx); r != nil {
		c.IP = s.clientIP(r)
	}
	sess, err := svc.LoginWithLine(s.auditContext(ctx), in, c)
	if err != nil {
		return nil, err
	}
	return api.StaffLoginWithLine200JSONResponse(api.StaffSession{
		SessionToken: sess.Token,
		TokenType:    api.StaffSessionTokenTypeBearer,
		ExpiresIn:    sess.ExpiresIn,
		Staff:        toStaffMe(sess.Staff),
		App:          toAppPublic(sess.App),
	}), nil
}

func (s *Server) GetStaffMe(ctx context.Context, _ api.GetStaffMeRequestObject) (api.GetStaffMeResponseObject, error) {
	p, err := requireStaff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.staffService()
	if err != nil {
		return nil, err
	}
	st, err := svc.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.GetStaffMe200JSONResponse(toStaffMe(st)), nil
}

func (s *Server) ResetStaffInvite(ctx context.Context, req api.ResetStaffInviteRequestObject) (api.ResetStaffInviteResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	created, err := s.tenants.ResetStaffInvite(ctx, p, uuid.UUID(req.StaffId), idem(ctx, req.Params.IdempotencyKey, nil))
	if err != nil {
		return nil, err
	}
	return api.ResetStaffInvite200JSONResponse(toStaffCreated(created)), nil
}

func (s *Server) EndMemberSessions(ctx context.Context, req api.EndMemberSessionsRequestObject) (api.EndMemberSessionsResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.tenants.EndMemberSessions(ctx, p, uuid.UUID(req.AppId), idem(ctx, req.Params.IdempotencyKey, nil))
	if err != nil {
		return nil, err
	}
	return api.EndMemberSessions200JSONResponse(s.toApp(app)), nil
}

func toStaffMe(st staffauth.Staff) api.StaffMe {
	out := api.StaffMe{
		Id:          openapi_types.UUID(st.ID),
		AppId:       openapi_types.UUID(st.AppID),
		DisplayName: st.DisplayName,
		Role:        api.StaffRole(st.Role),
	}
	if st.StoreID.Valid && st.StoreName != nil {
		out.Store = &api.StoreRef{Id: openapi_types.UUID(st.StoreID.UUID), Name: *st.StoreName}
	}
	return out
}

func toAppPublic(a sqlc.App) api.AppPublic {
	return api.AppPublic{
		Id:        openapi_types.UUID(a.ID),
		Name:      a.Name,
		Locale:    a.Locale,
		OaBasicId: a.OaBasicID,
	}
}
