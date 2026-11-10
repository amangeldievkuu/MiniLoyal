package httpserver

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/audit"
	"github.com/amangeldievkuu/miniloyal/internal/idempotency"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Handlers for tenants, team, apps, channels, stores, staff, API keys and the
// audit log. They authenticate, build the idempotency fingerprint and map
// types; the tenant service does the rest.

// dashboardCall authenticates a dashboard user and attaches request info
// for audit entries.
func (s *Server) dashboardCall(ctx context.Context) (context.Context, principal.Principal, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return ctx, principal.Principal{}, err
	}
	if s.tenants == nil {
		return ctx, principal.Principal{}, apierr.Unavailable("tenant service not configured")
	}
	return s.auditContext(ctx), p, nil
}

func (s *Server) auditContext(ctx context.Context) context.Context {
	info := audit.RequestInfo{RequestID: RequestIDFrom(ctx)}
	if r := httpRequest(ctx); r != nil {
		info.IP = s.clientIP(r)
	}
	return audit.WithRequestInfo(ctx, info)
}

// idem fingerprints the write: Idempotency-Key plus method, path and body.
func idem(ctx context.Context, key api.IdempotencyKey, body any) tenant.Idem {
	method, path := "", ""
	if r := httpRequest(ctx); r != nil {
		method, path = r.Method, r.URL.Path
	}
	raw, _ := json.Marshal(body)
	return tenant.Idem{Key: string(key), Hash: idempotency.Hash(method, path, raw)}
}

func requireBody[T any](body *T) (*T, error) {
	if body == nil {
		return nil, apierr.BadRequest("a JSON request body is required")
	}
	return body, nil
}

// ---- tenants

func (s *Server) ListTenants(ctx context.Context, _ api.ListTenantsRequestObject) (api.ListTenantsResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.tenants.ListMemberships(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make(api.ListTenants200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toMembership(r))
	}
	return out, nil
}

func (s *Server) GetTenant(ctx context.Context, req api.GetTenantRequestObject) (api.GetTenantResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.tenants.GetTenant(ctx, p, uuid.UUID(req.TenantId))
	if err != nil {
		return nil, err
	}
	return api.GetTenant200JSONResponse(toTenant(t)), nil
}

func (s *Server) UpdateTenant(ctx context.Context, req api.UpdateTenantRequestObject) (api.UpdateTenantResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	t, err := s.tenants.UpdateTenantName(ctx, p, uuid.UUID(req.TenantId), idem(ctx, req.Params.IdempotencyKey, body), body.Name)
	if err != nil {
		return nil, err
	}
	return api.UpdateTenant200JSONResponse(toTenant(t)), nil
}

// ---- team

func (s *Server) ListTeam(ctx context.Context, req api.ListTeamRequestObject) (api.ListTeamResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.tenants.ListTeam(ctx, p, uuid.UUID(req.TenantId))
	if err != nil {
		return nil, err
	}
	out := make(api.ListTeam200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, teamMemberFromList(r))
	}
	return out, nil
}

func (s *Server) UpdateTeamMember(ctx context.Context, req api.UpdateTeamMemberRequestObject) (api.UpdateTeamMemberResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	row, err := s.tenants.UpdateTeamMember(ctx, p, uuid.UUID(req.TenantId), uuid.UUID(req.UserId), idem(ctx, req.Params.IdempotencyKey, body), principal.Role(body.Role))
	if err != nil {
		return nil, err
	}
	return api.UpdateTeamMember200JSONResponse(teamMemberFromGet(row)), nil
}

func (s *Server) RemoveTeamMember(ctx context.Context, req api.RemoveTeamMemberRequestObject) (api.RemoveTeamMemberResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tenants.RemoveTeamMember(ctx, p, uuid.UUID(req.TenantId), uuid.UUID(req.UserId)); err != nil {
		return nil, err
	}
	return api.RemoveTeamMember204Response{}, nil
}

// ---- audit log

func (s *Server) ListAuditLog(ctx context.Context, req api.ListAuditLogRequestObject) (api.ListAuditLogResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	q := tenant.AuditQuery{AppID: uuidPtr(req.Params.AppId)}
	if req.Params.Limit != nil {
		q.Limit = int(*req.Params.Limit)
	}
	if req.Params.Cursor != nil {
		q.Cursor = string(*req.Params.Cursor)
	}
	page, err := s.tenants.ListAuditLog(ctx, p, uuid.UUID(req.TenantId), q)
	if err != nil {
		return nil, err
	}
	return api.ListAuditLog200JSONResponse(toAuditPage(page)), nil
}

// ---- apps

func (s *Server) ListApps(ctx context.Context, req api.ListAppsRequestObject) (api.ListAppsResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := s.tenants.ListApps(ctx, p, uuid.UUID(req.TenantId))
	if err != nil {
		return nil, err
	}
	return api.ListApps200JSONResponse(s.toApps(apps)), nil
}

func (s *Server) CreateApp(ctx context.Context, req api.CreateAppRequestObject) (api.CreateAppResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	app, err := s.tenants.CreateApp(ctx, p, uuid.UUID(req.TenantId), idem(ctx, req.Params.IdempotencyKey, body), tenant.AppCreate{
		Name: body.Name, Slug: body.Slug, Locale: body.Locale,
	})
	if err != nil {
		return nil, err
	}
	return api.CreateApp201JSONResponse(s.toApp(app)), nil
}

func (s *Server) GetApp(ctx context.Context, req api.GetAppRequestObject) (api.GetAppResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.tenants.GetApp(ctx, p, uuid.UUID(req.AppId))
	if err != nil {
		return nil, err
	}
	return api.GetApp200JSONResponse(s.toApp(app)), nil
}

func (s *Server) UpdateApp(ctx context.Context, req api.UpdateAppRequestObject) (api.UpdateAppResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	app, err := s.tenants.UpdateApp(ctx, p, uuid.UUID(req.AppId), idem(ctx, req.Params.IdempotencyKey, body), tenant.AppUpdate{
		Name: body.Name, Locale: body.Locale, Status: statusPtr(body.Status),
		OAChannelID: body.OaChannelId, OABasicID: body.OaBasicId, OAChannelSecret: body.OaChannelSecret,
		AllowHS256: body.AllowHs256IdTokens,
	})
	if err != nil {
		return nil, err
	}
	return api.UpdateApp200JSONResponse(s.toApp(app)), nil
}

// GetCurrentApp serves API keys: the app the key belongs to.
func (s *Server) GetCurrentApp(ctx context.Context, _ api.GetCurrentAppRequestObject) (api.GetCurrentAppResponseObject, error) {
	p, err := requireAPIKey(ctx, "")
	if err != nil {
		return nil, err
	}
	if s.tenants == nil {
		return nil, apierr.Unavailable("tenant service not configured")
	}
	app, err := s.tenants.CurrentApp(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.GetCurrentApp200JSONResponse(s.toApp(app)), nil
}

// ---- channels

func (s *Server) ListAppChannels(ctx context.Context, req api.ListAppChannelsRequestObject) (api.ListAppChannelsResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.tenants.ListChannels(ctx, p, uuid.UUID(req.AppId))
	if err != nil {
		return nil, err
	}
	out := make(api.ListAppChannels200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toChannel(r))
	}
	return out, nil
}

func (s *Server) PutAppChannel(ctx context.Context, req api.PutAppChannelRequestObject) (api.PutAppChannelResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	ch, err := s.tenants.PutChannel(ctx, p, uuid.UUID(req.AppId), idem(ctx, req.Params.IdempotencyKey, body), string(req.Environment), tenant.ChannelPut{
		ChannelID: body.ChannelId, LiffID: body.LiffId, Secret: body.ChannelSecret,
	})
	if err != nil {
		return nil, err
	}
	return api.PutAppChannel200JSONResponse(toChannel(ch)), nil
}

func (s *Server) DeleteAppChannel(ctx context.Context, req api.DeleteAppChannelRequestObject) (api.DeleteAppChannelResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tenants.DeleteChannel(ctx, p, uuid.UUID(req.AppId), string(req.Environment)); err != nil {
		return nil, err
	}
	return api.DeleteAppChannel204Response{}, nil
}

// ---- stores

func (s *Server) ListStores(ctx context.Context, req api.ListStoresRequestObject) (api.ListStoresResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.tenants.ListStores(ctx, p, uuid.UUID(req.AppId))
	if err != nil {
		return nil, err
	}
	out := make(api.ListStores200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toStore(r))
	}
	return out, nil
}

func (s *Server) CreateStore(ctx context.Context, req api.CreateStoreRequestObject) (api.CreateStoreResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	st, err := s.tenants.CreateStore(ctx, p, uuid.UUID(req.AppId), idem(ctx, req.Params.IdempotencyKey, body), tenant.StoreCreate{
		Name: body.Name, Code: body.Code, Address: body.Address,
	})
	if err != nil {
		return nil, err
	}
	return api.CreateStore201JSONResponse(toStore(st)), nil
}

func (s *Server) UpdateStore(ctx context.Context, req api.UpdateStoreRequestObject) (api.UpdateStoreResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	st, err := s.tenants.UpdateStore(ctx, p, uuid.UUID(req.StoreId), idem(ctx, req.Params.IdempotencyKey, body), tenant.StoreUpdate{
		Name: body.Name, Code: body.Code, Address: body.Address, Status: statusPtr(body.Status),
	})
	if err != nil {
		return nil, err
	}
	return api.UpdateStore200JSONResponse(toStore(st)), nil
}

func (s *Server) DeleteStore(ctx context.Context, req api.DeleteStoreRequestObject) (api.DeleteStoreResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tenants.DeleteStore(ctx, p, uuid.UUID(req.StoreId)); err != nil {
		return nil, err
	}
	return api.DeleteStore204Response{}, nil
}

// ---- staff

func (s *Server) ListStaff(ctx context.Context, req api.ListStaffRequestObject) (api.ListStaffResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.tenants.ListStaff(ctx, p, uuid.UUID(req.AppId))
	if err != nil {
		return nil, err
	}
	out := make(api.ListStaff200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toStaff(r))
	}
	return out, nil
}

func (s *Server) CreateStaff(ctx context.Context, req api.CreateStaffRequestObject) (api.CreateStaffResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	created, err := s.tenants.CreateStaff(ctx, p, uuid.UUID(req.AppId), idem(ctx, req.Params.IdempotencyKey, body), tenant.StaffCreate{
		DisplayName: body.DisplayName, StoreID: uuidPtr(body.StoreId), Role: staffRolePtr(body.Role),
	})
	if err != nil {
		return nil, err
	}
	return api.CreateStaff201JSONResponse(toStaffCreated(created)), nil
}

func (s *Server) UpdateStaff(ctx context.Context, req api.UpdateStaffRequestObject) (api.UpdateStaffResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	st, err := s.tenants.UpdateStaff(ctx, p, uuid.UUID(req.StaffId), idem(ctx, req.Params.IdempotencyKey, body), tenant.StaffUpdate{
		DisplayName: body.DisplayName, StoreID: uuidPtr(body.StoreId), Role: staffRolePtr(body.Role), Status: statusPtr(body.Status),
	})
	if err != nil {
		return nil, err
	}
	return api.UpdateStaff200JSONResponse(toStaff(st)), nil
}

func (s *Server) DeleteStaff(ctx context.Context, req api.DeleteStaffRequestObject) (api.DeleteStaffResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tenants.DeleteStaff(ctx, p, uuid.UUID(req.StaffId)); err != nil {
		return nil, err
	}
	return api.DeleteStaff204Response{}, nil
}

// ---- API keys

func (s *Server) ListApiKeys(ctx context.Context, req api.ListApiKeysRequestObject) (api.ListApiKeysResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.tenants.ListAPIKeys(ctx, p, uuid.UUID(req.AppId))
	if err != nil {
		return nil, err
	}
	out := make(api.ListApiKeys200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAPIKey(r))
	}
	return out, nil
}

func (s *Server) CreateApiKey(ctx context.Context, req api.CreateApiKeyRequestObject) (api.CreateApiKeyResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	body, err := requireBody(req.Body)
	if err != nil {
		return nil, err
	}
	created, err := s.tenants.CreateAPIKey(ctx, p, uuid.UUID(req.AppId), idem(ctx, req.Params.IdempotencyKey, body), body.Name, fromScopes(body.Scopes))
	if err != nil {
		return nil, err
	}
	return api.CreateApiKey201JSONResponse(toAPIKeyCreated(created)), nil
}

func (s *Server) RevokeApiKey(ctx context.Context, req api.RevokeApiKeyRequestObject) (api.RevokeApiKeyResponseObject, error) {
	ctx, p, err := s.dashboardCall(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.tenants.RevokeAPIKey(ctx, p, uuid.UUID(req.KeyId)); err != nil {
		return nil, err
	}
	return api.RevokeApiKey204Response{}, nil
}
