import { queryOptions } from '@tanstack/react-query';

import { api, unwrap } from './client';
import type {
  ApiKey,
  App,
  AppChannel,
  AuditPage,
  Me,
  Membership,
  Staff,
  Store,
  TeamMember,
  Tenant,
} from './types';

export const keys = {
  me: ['me'] as const,
  tenants: ['tenants'] as const,
  tenant: (id: string) => ['tenant', id] as const,
  team: (tenantId: string) => ['tenant', tenantId, 'team'] as const,
  audit: (tenantId: string, appId: string | undefined, cursor: string | undefined) =>
    ['tenant', tenantId, 'audit', appId ?? '', cursor ?? ''] as const,
  apps: (tenantId: string) => ['tenant', tenantId, 'apps'] as const,
  app: (appId: string) => ['app', appId] as const,
  channels: (appId: string) => ['app', appId, 'channels'] as const,
  stores: (appId: string) => ['app', appId, 'stores'] as const,
  staff: (appId: string) => ['app', appId, 'staff'] as const,
  apiKeys: (appId: string) => ['app', appId, 'api-keys'] as const,
};

export const meQuery = queryOptions({
  queryKey: keys.me,
  queryFn: async (): Promise<Me> => unwrap(await api.GET('/v1/dashboard/me')),
});

export const tenantsQuery = queryOptions({
  queryKey: keys.tenants,
  queryFn: async (): Promise<Membership[]> => unwrap(await api.GET('/v1/dashboard/tenants')),
});

export const tenantQuery = (tenantId: string) =>
  queryOptions({
    queryKey: keys.tenant(tenantId),
    queryFn: async (): Promise<Tenant> =>
      unwrap(await api.GET('/v1/dashboard/tenants/{tenantId}', { params: { path: { tenantId } } })),
  });

export const teamQuery = (tenantId: string) =>
  queryOptions({
    queryKey: keys.team(tenantId),
    queryFn: async (): Promise<TeamMember[]> =>
      unwrap(
        await api.GET('/v1/dashboard/tenants/{tenantId}/team', { params: { path: { tenantId } } }),
      ),
  });

export const auditQuery = (
  tenantId: string,
  appId: string | undefined,
  cursor: string | undefined,
) =>
  queryOptions({
    queryKey: keys.audit(tenantId, appId, cursor),
    queryFn: async (): Promise<AuditPage> =>
      unwrap(
        await api.GET('/v1/dashboard/tenants/{tenantId}/audit-log', {
          params: {
            path: { tenantId },
            query: {
              limit: 50,
              ...(cursor ? { cursor } : {}),
              ...(appId ? { app_id: appId } : {}),
            },
          },
        }),
      ),
  });

export const appsQuery = (tenantId: string) =>
  queryOptions({
    queryKey: keys.apps(tenantId),
    queryFn: async (): Promise<App[]> =>
      unwrap(
        await api.GET('/v1/dashboard/tenants/{tenantId}/apps', { params: { path: { tenantId } } }),
      ),
  });

export const appQuery = (appId: string) =>
  queryOptions({
    queryKey: keys.app(appId),
    queryFn: async (): Promise<App> =>
      unwrap(await api.GET('/v1/dashboard/apps/{appId}', { params: { path: { appId } } })),
  });

export const channelsQuery = (appId: string) =>
  queryOptions({
    queryKey: keys.channels(appId),
    queryFn: async (): Promise<AppChannel[]> =>
      unwrap(await api.GET('/v1/dashboard/apps/{appId}/channels', { params: { path: { appId } } })),
  });

export const storesQuery = (appId: string) =>
  queryOptions({
    queryKey: keys.stores(appId),
    queryFn: async (): Promise<Store[]> =>
      unwrap(await api.GET('/v1/dashboard/apps/{appId}/stores', { params: { path: { appId } } })),
  });

export const staffQuery = (appId: string) =>
  queryOptions({
    queryKey: keys.staff(appId),
    queryFn: async (): Promise<Staff[]> =>
      unwrap(await api.GET('/v1/dashboard/apps/{appId}/staff', { params: { path: { appId } } })),
  });

export const apiKeysQuery = (appId: string) =>
  queryOptions({
    queryKey: keys.apiKeys(appId),
    queryFn: async (): Promise<ApiKey[]> =>
      unwrap(await api.GET('/v1/dashboard/apps/{appId}/api-keys', { params: { path: { appId } } })),
  });
