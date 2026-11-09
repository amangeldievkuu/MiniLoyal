import { Link, Outlet, useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';

import { useMe, sessionStore } from '../auth/session';
import { useCurrentTenant } from '../auth/tenant';
import { LANGUAGES, setLanguage, type Language } from '../i18n';
import { Button, Select } from './ui';

export function AppShell() {
  const { t, i18n } = useTranslation();
  const me = useMe();
  const navigate = useNavigate();
  const currentTenant = useCurrentTenant();
  const memberships = me?.memberships ?? [];
  const tenantId = memberships.some((m) => m.tenant.id === currentTenant)
    ? currentTenant
    : (memberships[0]?.tenant.id ?? null);

  const navLink =
    'rounded-md px-3 py-2 text-sm font-medium text-zinc-700 hover:bg-zinc-100 data-[status=active]:bg-brand-50 data-[status=active]:text-brand-700 dark:text-zinc-200 dark:hover:bg-zinc-800 dark:data-[status=active]:bg-zinc-800 dark:data-[status=active]:text-brand-500';

  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
          <Link to="/" className="text-lg font-bold text-brand-700 dark:text-brand-500">
            {t('brand')}
          </Link>
          {memberships.length > 0 && (
            <label className="flex items-center gap-2 text-sm">
              <span className="sr-only">{t('nav.tenant')}</span>
              <Select
                aria-label={t('nav.tenant')}
                className="w-auto min-w-40"
                value={tenantId ?? ''}
                onChange={(e) =>
                  void navigate({ to: '/t/$tenantId', params: { tenantId: e.target.value } })
                }
              >
                {memberships.map((m) => (
                  <option key={m.tenant.id} value={m.tenant.id}>
                    {m.tenant.name}
                  </option>
                ))}
              </Select>
            </label>
          )}
          <nav aria-label="Main" className="flex flex-wrap items-center gap-1">
            {tenantId && (
              <>
                <Link
                  to="/t/$tenantId"
                  params={{ tenantId }}
                  activeOptions={{ exact: true }}
                  className={navLink}
                >
                  {t('nav.apps')}
                </Link>
                <Link to="/t/$tenantId/team" params={{ tenantId }} className={navLink}>
                  {t('nav.team')}
                </Link>
                <Link to="/t/$tenantId/audit" params={{ tenantId }} className={navLink}>
                  {t('nav.audit')}
                </Link>
              </>
            )}
            <Link to="/me" className={navLink}>
              {t('nav.account')}
            </Link>
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <label className="flex items-center gap-2 text-sm">
              <span className="sr-only">{t('nav.language')}</span>
              <Select
                aria-label={t('nav.language')}
                className="w-auto"
                value={i18n.language}
                onChange={(e) => setLanguage(e.target.value as Language)}
              >
                {LANGUAGES.map((l) => (
                  <option key={l.code} value={l.code}>
                    {l.label}
                  </option>
                ))}
              </Select>
            </label>
            <Button
              variant="ghost"
              onClick={() => {
                void sessionStore.logout().then(() => navigate({ to: '/login' }));
              }}
            >
              {t('common.signOut')}
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}
