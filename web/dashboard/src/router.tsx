import {
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
} from '@tanstack/react-router';

import { sessionStore } from './auth/session';
import { lastTenant } from './auth/tenant';
import { AppShell } from './components/AppShell';
import { AcceptInvitePage } from './pages/AcceptInvitePage';
import { ApiKeysPage } from './pages/ApiKeysPage';
import { AppChannelsPage } from './pages/AppChannelsPage';
import { AppSettingsPage } from './pages/AppSettingsPage';
import { AuditPage } from './pages/AuditPage';
import { LoginPage, RegisterPage } from './pages/AuthPages';
import { MePage } from './pages/MePage';
import { MemberDetailPage } from './pages/MemberDetailPage';
import { MembersPage } from './pages/MembersPage';
import { StaffPage } from './pages/StaffPage';
import { StoresPage } from './pages/StoresPage';
import { TeamPage } from './pages/TeamPage';
import { TenantAppsPage } from './pages/TenantAppsPage';
import { NoTenantPage, NotFoundPage } from './pages/misc';

const rootRoute = createRootRoute({
  component: Outlet,
  notFoundComponent: NotFoundPage,
});

/** Signed-in users do not need the auth pages. */
async function redirectIfAuthenticated() {
  await sessionStore.bootstrap();
  if (sessionStore.get().status === 'authenticated') {
    throw redirect({ to: '/' });
  }
}

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: (search: Record<string, unknown>): { redirect?: string } => {
    const target = search['redirect'];
    return typeof target === 'string' ? { redirect: target } : {};
  },
  beforeLoad: redirectIfAuthenticated,
  component: LoginPage,
});

const registerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/register',
  beforeLoad: redirectIfAuthenticated,
  component: RegisterPage,
});

/** Invitation links land here; a signed-in user may accept one for another tenant. */
const acceptInviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/accept-invite',
  validateSearch: (search: Record<string, unknown>): { token: string } => {
    const token = search['token'];
    return { token: typeof token === 'string' ? token : '' };
  },
  component: AcceptInvitePage,
});

/** Everything below needs a session; the shell renders around it. */
const authRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'authenticated',
  beforeLoad: async ({ location }) => {
    await sessionStore.bootstrap();
    if (sessionStore.get().status !== 'authenticated') {
      throw redirect({ to: '/login', search: { redirect: location.href } });
    }
  },
  component: AppShell,
});

const indexRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/',
  beforeLoad: () => {
    const state = sessionStore.get();
    if (state.status !== 'authenticated') return;
    const memberships = state.me.memberships;
    const remembered = lastTenant();
    const tenantId = memberships.some((m) => m.tenant.id === remembered)
      ? remembered
      : (memberships[0]?.tenant.id ?? null);
    if (tenantId) throw redirect({ to: '/t/$tenantId', params: { tenantId } });
  },
  component: NoTenantPage,
});

const tenantRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/t/$tenantId',
  component: TenantAppsPage,
});

const teamRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/t/$tenantId/team',
  component: TeamPage,
});

const auditRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/t/$tenantId/audit',
  component: AuditPage,
});

const appRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/apps/$appId',
  component: AppSettingsPage,
});

const membersRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/apps/$appId/members',
  component: MembersPage,
});

const memberDetailRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/apps/$appId/members/$memberId',
  component: MemberDetailPage,
});

const channelsRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/apps/$appId/channels',
  component: AppChannelsPage,
});

const storesRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/apps/$appId/stores',
  component: StoresPage,
});

const staffRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/apps/$appId/staff',
  component: StaffPage,
});

const apiKeysRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/apps/$appId/api-keys',
  component: ApiKeysPage,
});

const meRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/me',
  component: MePage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  registerRoute,
  acceptInviteRoute,
  authRoute.addChildren([
    indexRoute,
    tenantRoute,
    teamRoute,
    auditRoute,
    appRoute,
    membersRoute,
    memberDetailRoute,
    channelsRoute,
    storesRoute,
    staffRoute,
    apiKeysRoute,
    meRoute,
  ]),
]);

export const router = createRouter({ routeTree, defaultPreload: 'intent' });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
