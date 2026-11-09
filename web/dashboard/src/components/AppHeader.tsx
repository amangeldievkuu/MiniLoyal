import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { appQuery } from '../api/queries';
import type { App, Role } from '../api/types';
import { useMe } from '../auth/session';
import { useDeclareTenant } from '../auth/tenant';
import { roleIn } from '../lib/roles';
import { ErrorAlert } from './feedback';
import { Badge, Status } from './ui';

const tabs = [
  { to: '/apps/$appId', key: 'settings' },
  { to: '/apps/$appId/members', key: 'members' },
  { to: '/apps/$appId/channels', key: 'channels' },
  { to: '/apps/$appId/stores', key: 'stores' },
  { to: '/apps/$appId/staff', key: 'staff' },
  { to: '/apps/$appId/api-keys', key: 'apiKeys' },
] as const;

/**
 * Loads the app, declares its tenant for the shell and renders the tab bar.
 * Children receive the app and the caller's role in its tenant.
 */
export function AppHeader({
  appId,
  children,
}: {
  appId: string;
  children: (app: App, role: Role | null) => ReactNode;
}) {
  const { t } = useTranslation();
  const me = useMe();
  const app = useQuery(appQuery(appId));
  useDeclareTenant(app.data?.tenant_id);

  if (app.isPending) return <Status>{t('common.loading')}</Status>;
  if (app.isError) return <ErrorAlert error={app.error} />;

  const role = roleIn(me, app.data.tenant_id);
  const tabClass =
    'rounded-md px-3 py-2 text-sm font-medium whitespace-nowrap text-zinc-700 hover:bg-zinc-100 data-[status=active]:bg-brand-50 data-[status=active]:text-brand-700 dark:text-zinc-200 dark:hover:bg-zinc-800 dark:data-[status=active]:bg-zinc-800 dark:data-[status=active]:text-brand-500';

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Link
          to="/t/$tenantId"
          params={{ tenantId: app.data.tenant_id }}
          className="text-sm text-zinc-600 hover:underline dark:text-zinc-400"
        >
          ← {t('nav.backToTenant')}
        </Link>
        <h1 className="text-xl font-semibold sm:text-2xl">{app.data.name}</h1>
        <Badge tone={app.data.status === 'active' ? 'success' : 'warning'}>
          {t(`statuses.${app.data.status}`)}
        </Badge>
      </div>
      <nav aria-label="App sections" className="mb-6 -mx-4 overflow-x-auto px-4 sm:mx-0 sm:px-0">
        <div className="flex gap-1 border-b border-zinc-200 pb-2 dark:border-zinc-800">
          {tabs.map((tab) => (
            <Link
              key={tab.key}
              to={tab.to}
              params={{ appId }}
              activeOptions={{ exact: tab.key === 'settings' }}
              className={tabClass}
            >
              {t(`appPage.tabs.${tab.key}`)}
            </Link>
          ))}
        </div>
      </nav>
      {children(app.data, role)}
    </div>
  );
}
