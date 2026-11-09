package httpserver

// Phase 1 stubs: tenants, team, apps, channels, stores, staff, API keys, audit log.
// Each method answers 501 until its feature lands; delete the method here when
// you implement it in its own file.

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

func (s *Server) GetCurrentApp(context.Context, api.GetCurrentAppRequestObject) (api.GetCurrentAppResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) RevokeApiKey(context.Context, api.RevokeApiKeyRequestObject) (api.RevokeApiKeyResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetApp(context.Context, api.GetAppRequestObject) (api.GetAppResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateApp(context.Context, api.UpdateAppRequestObject) (api.UpdateAppResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListApiKeys(context.Context, api.ListApiKeysRequestObject) (api.ListApiKeysResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) CreateApiKey(context.Context, api.CreateApiKeyRequestObject) (api.CreateApiKeyResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListAppChannels(context.Context, api.ListAppChannelsRequestObject) (api.ListAppChannelsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DeleteAppChannel(context.Context, api.DeleteAppChannelRequestObject) (api.DeleteAppChannelResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) PutAppChannel(context.Context, api.PutAppChannelRequestObject) (api.PutAppChannelResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListStaff(context.Context, api.ListStaffRequestObject) (api.ListStaffResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) CreateStaff(context.Context, api.CreateStaffRequestObject) (api.CreateStaffResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListStores(context.Context, api.ListStoresRequestObject) (api.ListStoresResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) CreateStore(context.Context, api.CreateStoreRequestObject) (api.CreateStoreResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DeleteStaff(context.Context, api.DeleteStaffRequestObject) (api.DeleteStaffResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateStaff(context.Context, api.UpdateStaffRequestObject) (api.UpdateStaffResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) DeleteStore(context.Context, api.DeleteStoreRequestObject) (api.DeleteStoreResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateStore(context.Context, api.UpdateStoreRequestObject) (api.UpdateStoreResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListTenants(context.Context, api.ListTenantsRequestObject) (api.ListTenantsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetTenant(context.Context, api.GetTenantRequestObject) (api.GetTenantResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateTenant(context.Context, api.UpdateTenantRequestObject) (api.UpdateTenantResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListApps(context.Context, api.ListAppsRequestObject) (api.ListAppsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) CreateApp(context.Context, api.CreateAppRequestObject) (api.CreateAppResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListAuditLog(context.Context, api.ListAuditLogRequestObject) (api.ListAuditLogResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListTeam(context.Context, api.ListTeamRequestObject) (api.ListTeamResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) AddTeamMember(context.Context, api.AddTeamMemberRequestObject) (api.AddTeamMemberResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) RemoveTeamMember(context.Context, api.RemoveTeamMemberRequestObject) (api.RemoveTeamMemberResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) UpdateTeamMember(context.Context, api.UpdateTeamMemberRequestObject) (api.UpdateTeamMemberResponseObject, error) {
	return nil, errNotImplemented
}
