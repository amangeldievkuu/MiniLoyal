package httpserver

import (
	"encoding/json"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/amangeldievkuu/miniloyal/internal/api"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// Mapping from storage rows to API documents. Secrets and hashes never cross
// this boundary; only whether a secret is set.

func toTenant(t sqlc.Tenant) api.Tenant {
	out := api.Tenant{
		Id:        openapi_types.UUID(t.ID),
		Type:      api.TenantType(t.Type),
		Name:      t.Name,
		Slug:      t.Slug,
		Plan:      t.Plan,
		CreatedAt: t.CreatedAt.Time,
	}
	if t.ParentTenantID.Valid {
		id := openapi_types.UUID(t.ParentTenantID.UUID)
		out.ParentTenantId = &id
	}
	return out
}

func toMembership(row sqlc.ListMembershipsForUserRow) api.Membership {
	return api.Membership{Tenant: toTenant(row.Tenant), Role: api.Role(row.Role)}
}

func toTeamMember(userID uuid.UUID, email string, displayName *string, role string, created sqlc.Tenant) api.TeamMember {
	_ = created
	return api.TeamMember{UserId: openapi_types.UUID(userID), Email: openapi_types.Email(email), DisplayName: displayName, Role: api.Role(role)}
}

func teamMemberFromList(r sqlc.ListTeamRow) api.TeamMember {
	m := toTeamMember(r.UserID, r.Email, r.DisplayName, r.Role, sqlc.Tenant{})
	m.CreatedAt = r.CreatedAt.Time
	return m
}

func teamMemberFromGet(r sqlc.GetTeamMemberRow) api.TeamMember {
	m := toTeamMember(r.UserID, r.Email, r.DisplayName, r.Role, sqlc.Tenant{})
	m.CreatedAt = r.CreatedAt.Time
	return m
}

func toApp(a sqlc.App) api.App {
	return api.App{
		Id:                 openapi_types.UUID(a.ID),
		TenantId:           openapi_types.UUID(a.TenantID),
		Name:               a.Name,
		Slug:               a.Slug,
		Locale:             a.Locale,
		Status:             api.AppStatus(a.Status),
		OaChannelId:        a.OaChannelID,
		OaBasicId:          a.OaBasicID,
		OaChannelSecretSet: len(a.OaChannelSecretEnc) > 0,
		CreatedAt:          a.CreatedAt.Time,
		UpdatedAt:          a.UpdatedAt.Time,
	}
}

func toApps(rows []sqlc.App) []api.App {
	out := make([]api.App, 0, len(rows))
	for _, r := range rows {
		out = append(out, toApp(r))
	}
	return out
}

func toChannel(c sqlc.AppChannel) api.AppChannel {
	return api.AppChannel{
		Environment:      api.ChannelEnvironment(c.Environment),
		ChannelId:        c.ChannelID,
		LiffId:           c.LiffID,
		ChannelSecretSet: len(c.ChannelSecretEnc) > 0,
		CreatedAt:        c.CreatedAt.Time,
	}
}

func toStore(st sqlc.Store) api.Store {
	return api.Store{
		Id:        openapi_types.UUID(st.ID),
		AppId:     openapi_types.UUID(st.AppID),
		Name:      st.Name,
		Code:      st.Code,
		Address:   st.Address,
		Status:    api.AppStatus(st.Status),
		CreatedAt: st.CreatedAt.Time,
	}
}

func toStaff(st sqlc.Staff) api.Staff {
	out := api.Staff{
		Id:          openapi_types.UUID(st.ID),
		AppId:       openapi_types.UUID(st.AppID),
		DisplayName: st.DisplayName,
		Role:        api.StaffRole(st.Role),
		Status:      api.AppStatus(st.Status),
		LineLinked:  st.LineUserID != nil && *st.LineUserID != "",
		CreatedAt:   st.CreatedAt.Time,
	}
	if st.StoreID.Valid {
		id := openapi_types.UUID(st.StoreID.UUID)
		out.StoreId = &id
	}
	return out
}

func toStaffCreated(c tenant.StaffCreated) api.StaffCreated {
	st := toStaff(c.Staff)
	return api.StaffCreated{
		Id: st.Id, AppId: st.AppId, DisplayName: st.DisplayName, Role: st.Role, Status: st.Status,
		LineLinked: st.LineLinked, StoreId: st.StoreId, CreatedAt: st.CreatedAt,
		InviteCode: c.InviteCode,
	}
}

func toAPIKey(k sqlc.ApiKey) api.ApiKey {
	out := api.ApiKey{
		Id:        openapi_types.UUID(k.ID),
		AppId:     openapi_types.UUID(k.AppID),
		Name:      k.Name,
		Prefix:    k.Prefix,
		Scopes:    toScopes(k.Scopes),
		CreatedAt: k.CreatedAt.Time,
	}
	if k.LastUsedAt.Valid {
		t := k.LastUsedAt.Time
		out.LastUsedAt = &t
	}
	return out
}

func toAPIKeyCreated(c tenant.APIKeyCreated) api.ApiKeyCreated {
	k := toAPIKey(c.ApiKey)
	return api.ApiKeyCreated{
		Id: k.Id, AppId: k.AppId, Name: k.Name, Prefix: k.Prefix, Scopes: k.Scopes,
		LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt, Key: c.Key,
	}
}

func toScopes(in []string) []api.ApiKeyScope {
	out := make([]api.ApiKeyScope, 0, len(in))
	for _, s := range in {
		out = append(out, api.ApiKeyScope(s))
	}
	return out
}

func fromScopes(in []api.ApiKeyScope) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}

func toAuditEntry(e sqlc.AuditLog) api.AuditEntry {
	out := api.AuditEntry{
		Id:         e.ID,
		ActorType:  api.AuditActorType(e.ActorType),
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetId:   e.TargetID,
		RequestId:  e.RequestID,
		CreatedAt:  e.CreatedAt.Time,
	}
	if e.AppID.Valid {
		id := openapi_types.UUID(e.AppID.UUID)
		out.AppId = &id
	}
	if e.ActorID.Valid {
		id := openapi_types.UUID(e.ActorID.UUID)
		out.ActorId = &id
	}
	if len(e.Metadata) > 0 {
		var meta map[string]any
		if err := json.Unmarshal(e.Metadata, &meta); err == nil {
			out.Metadata = &meta
		}
	}
	return out
}

func toAuditPage(p tenant.AuditPage) api.AuditPage {
	out := api.AuditPage{Items: make([]api.AuditEntry, 0, len(p.Items))}
	for _, e := range p.Items {
		out.Items = append(out.Items, toAuditEntry(e))
	}
	if p.NextCursor != "" {
		c := p.NextCursor
		out.NextCursor = &c
	}
	return out
}

func uuidPtr(v *openapi_types.UUID) *uuid.UUID {
	if v == nil {
		return nil
	}
	id := uuid.UUID(*v)
	return &id
}

func statusPtr(v *api.AppStatus) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

func staffRolePtr(v *api.StaffRole) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}
