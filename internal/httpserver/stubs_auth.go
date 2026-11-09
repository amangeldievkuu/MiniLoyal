package httpserver

// Phase 1 stubs: dashboard authentication and the signed-in user.
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) DashboardLogin(context.Context, api.DashboardLoginRequestObject) (api.DashboardLoginResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DashboardLogout(context.Context, api.DashboardLogoutRequestObject) (api.DashboardLogoutResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DashboardMfa(context.Context, api.DashboardMfaRequestObject) (api.DashboardMfaResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DashboardRefresh(context.Context, api.DashboardRefreshRequestObject) (api.DashboardRefreshResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DashboardRegister(context.Context, api.DashboardRegisterRequestObject) (api.DashboardRegisterResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetDashboardMe(context.Context, api.GetDashboardMeRequestObject) (api.GetDashboardMeResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DashboardLogoutAll(context.Context, api.DashboardLogoutAllRequestObject) (api.DashboardLogoutAllResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ChangeDashboardPassword(context.Context, api.ChangeDashboardPasswordRequestObject) (api.ChangeDashboardPasswordResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DisableTotp(context.Context, api.DisableTotpRequestObject) (api.DisableTotpResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ConfirmTotp(context.Context, api.ConfirmTotpRequestObject) (api.ConfirmTotpResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) EnrollTotp(context.Context, api.EnrollTotpRequestObject) (api.EnrollTotpResponseObject, error) {
	return nil, errNotImplemented
}
