package httpserver

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/auth"
)

// Dashboard authentication handlers. They stay thin: decode, call the auth
// service, set the refresh cookie, encode. Errors are *apierr.Error values
// the response error handler renders.

func (s *Server) authService() (*auth.Service, error) {
	if s.auth == nil {
		return nil, apierr.Unavailable("authentication is not configured")
	}
	return s.auth, nil
}

// authClient describes the calling device for session records and limits.
func (s *Server) authClient(ctx context.Context) auth.Client {
	c := auth.Client{UserAgent: userAgent(ctx)}
	if r := httpRequest(ctx); r != nil {
		c.IP = s.clientIP(r)
	}
	return c
}

func (s *Server) DashboardRegister(ctx context.Context, req api.DashboardRegisterRequestObject) (api.DashboardRegisterResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierr.BadRequest("request body is required")
	}
	sess, err := svc.Register(ctx, auth.RegisterInput{
		Email:       string(req.Body.Email),
		Password:    req.Body.Password,
		DisplayName: req.Body.DisplayName,
		TenantName:  req.Body.TenantName,
		TenantType:  string(req.Body.TenantType),
	}, s.authClient(ctx))
	if err != nil {
		return nil, err
	}
	s.setRefreshCookie(ctx, sess.RefreshToken)
	return api.DashboardRegister201JSONResponse(toAPISession(sess)), nil
}

func (s *Server) DashboardLogin(ctx context.Context, req api.DashboardLoginRequestObject) (api.DashboardLoginResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierr.BadRequest("request body is required")
	}
	res, err := svc.Login(ctx, string(req.Body.Email), req.Body.Password, s.authClient(ctx))
	if err != nil {
		return nil, err
	}
	var out api.LoginResult
	if res.Session != nil {
		s.setRefreshCookie(ctx, res.Session.RefreshToken)
		sess := toAPISession(res.Session)
		out.Session = &sess
	}
	if res.MFA != nil {
		out.Mfa = &api.MfaChallenge{MfaToken: res.MFA.Token, ExpiresIn: res.MFA.ExpiresIn}
	}
	return api.DashboardLogin200JSONResponse(out), nil
}

func (s *Server) DashboardMfa(ctx context.Context, req api.DashboardMfaRequestObject) (api.DashboardMfaResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierr.BadRequest("request body is required")
	}
	sess, err := svc.CompleteMFA(ctx, req.Body.MfaToken, req.Body.Code, s.authClient(ctx))
	if err != nil {
		return nil, err
	}
	s.setRefreshCookie(ctx, sess.RefreshToken)
	return api.DashboardMfa200JSONResponse(toAPISession(sess)), nil
}

func (s *Server) DashboardRefresh(ctx context.Context, _ api.DashboardRefreshRequestObject) (api.DashboardRefreshResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	sess, err := svc.Refresh(ctx, s.refreshCookie(ctx), s.authClient(ctx))
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Status == 401 {
			s.clearRefreshCookie(ctx)
		}
		return nil, err
	}
	s.setRefreshCookie(ctx, sess.RefreshToken)
	return api.DashboardRefresh200JSONResponse(toAPISession(sess)), nil
}

func (s *Server) DashboardLogout(ctx context.Context, _ api.DashboardLogoutRequestObject) (api.DashboardLogoutResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	if err := svc.Logout(ctx, s.refreshCookie(ctx)); err != nil {
		return nil, err
	}
	s.clearRefreshCookie(ctx)
	return api.DashboardLogout204Response{}, nil
}

func (s *Server) GetDashboardMe(ctx context.Context, _ api.GetDashboardMeRequestObject) (api.GetDashboardMeResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	user, memberships, err := svc.Me(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := api.Me{User: toAPIUser(*user), Memberships: make([]api.Membership, 0, len(memberships))}
	for _, m := range memberships {
		out.Memberships = append(out.Memberships, api.Membership{Tenant: toAPITenant(m.Tenant), Role: api.Role(m.Role)})
	}
	return api.GetDashboardMe200JSONResponse(out), nil
}

func (s *Server) ChangeDashboardPassword(ctx context.Context, req api.ChangeDashboardPasswordRequestObject) (api.ChangeDashboardPasswordResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierr.BadRequest("request body is required")
	}
	sess, err := svc.ChangePassword(ctx, p.UserID, req.Body.CurrentPassword, req.Body.NewPassword, s.authClient(ctx))
	if err != nil {
		return nil, err
	}
	s.setRefreshCookie(ctx, sess.RefreshToken)
	return api.ChangeDashboardPassword200JSONResponse(toAPISession(sess)), nil
}

func (s *Server) DashboardLogoutAll(ctx context.Context, _ api.DashboardLogoutAllRequestObject) (api.DashboardLogoutAllResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.LogoutAll(s.withDevice(ctx), p.UserID); err != nil {
		return nil, err
	}
	s.clearRefreshCookie(ctx)
	return api.DashboardLogoutAll204Response{}, nil
}

func (s *Server) EnrollTotp(ctx context.Context, _ api.EnrollTotpRequestObject) (api.EnrollTotpResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	secret, url, err := svc.EnrollTOTP(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return api.EnrollTotp200JSONResponse(api.TotpEnrollment{Secret: secret, OtpauthUrl: url}), nil
}

func (s *Server) ConfirmTotp(ctx context.Context, req api.ConfirmTotpRequestObject) (api.ConfirmTotpResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierr.BadRequest("request body is required")
	}
	codes, err := svc.ConfirmTOTP(s.withDevice(ctx), p.UserID, req.Body.Code)
	if err != nil {
		return nil, err
	}
	return api.ConfirmTotp200JSONResponse(api.TotpConfirmed{RecoveryCodes: codes}), nil
}

func (s *Server) DisableTotp(ctx context.Context, req api.DisableTotpRequestObject) (api.DisableTotpResponseObject, error) {
	svc, err := s.authService()
	if err != nil {
		return nil, err
	}
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierr.BadRequest("request body is required")
	}
	if err := svc.DisableTOTP(s.withDevice(ctx), p.UserID, req.Body.Password, req.Body.Code); err != nil {
		return nil, err
	}
	return api.DisableTotp204Response{}, nil
}

func toAPIUser(u auth.User) api.User {
	return api.User{
		Id:          u.ID,
		Email:       openapi_types.Email(u.Email),
		DisplayName: u.DisplayName,
		TotpEnabled: u.TotpEnabled,
		CreatedAt:   u.CreatedAt,
	}
}

func toAPISession(s *auth.Session) api.Session {
	return api.Session{
		AccessToken: s.AccessToken,
		ExpiresIn:   s.ExpiresIn,
		TokenType:   api.SessionTokenTypeBearer,
		User:        toAPIUser(s.User),
	}
}

func toAPITenant(t auth.Tenant) api.Tenant {
	return api.Tenant{
		Id:             t.ID,
		Type:           api.TenantType(t.Type),
		Name:           t.Name,
		Slug:           t.Slug,
		Plan:           t.Plan,
		ParentTenantId: t.ParentTenantID,
		CreatedAt:      t.CreatedAt,
	}
}
