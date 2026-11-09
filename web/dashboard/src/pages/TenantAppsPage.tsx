import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { appsQuery, keys, tenantQuery } from '../api/queries';
import { LOCALES, type AppCreate } from '../api/types';
import { sessionStore, useMe } from '../auth/session';
import { useDeclareTenant } from '../auth/tenant';
import { EmptyState, ErrorAlert, PageHeader, ReadOnlyNotice } from '../components/feedback';
import { Badge, Button, Card, Dialog, Field, Input, SelectField, Status } from '../components/ui';
import { canManage, roleIn } from '../lib/roles';

export function TenantAppsPage() {
  const { t } = useTranslation();
  const { tenantId = '' } = useParams({ strict: false });
  useDeclareTenant(tenantId);
  const me = useMe();
  const role = roleIn(me, tenantId);
  const qc = useQueryClient();
  const tenant = useQuery(tenantQuery(tenantId));
  const apps = useQuery(appsQuery(tenantId));
  const [open, setOpen] = useState(false);

  const create = useIdempotentMutation(
    async (body: AppCreate, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/tenants/{tenantId}/apps', {
          params: { path: { tenantId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: keys.apps(tenantId) });
        setOpen(false);
      },
    },
  );

  const rename = useIdempotentMutation(
    async (name: string, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/tenants/{tenantId}', {
          params: { path: { tenantId }, header: { 'Idempotency-Key': key } },
          body: { name },
        }),
      ),
    {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: keys.tenant(tenantId) });
        void sessionStore.reloadMe();
      },
    },
  );

  function submitCreate(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const slug = String(fd.get('slug') ?? '').trim();
    create.mutate({
      name: String(fd.get('name') ?? '').trim(),
      locale: String(fd.get('locale') ?? 'zh-TW'),
      ...(slug ? { slug } : {}),
    });
  }

  function submitRename(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    rename.mutate(String(fd.get('name') ?? '').trim());
  }

  const createError = asApiError(create.error);

  return (
    <div>
      <PageHeader
        title={tenant.data?.name ?? t('tenant.apps.title')}
        description={tenant.data ? t('tenant.plan', { plan: tenant.data.plan }) : undefined}
        actions={
          canManage(role) && (
            <Button onClick={() => setOpen(true)}>{t('tenant.apps.create')}</Button>
          )
        }
      />
      {role === 'staff' && <ReadOnlyNotice />}
      {tenant.isError && <ErrorAlert error={tenant.error} />}

      {canManage(role) && tenant.data && (
        <Card title={t('tenant.rename')} className="mb-6">
          <form onSubmit={submitRename} className="flex flex-wrap items-end gap-3">
            <Field
              id="tenant-name"
              name="name"
              label={t('common.name')}
              defaultValue={tenant.data.name}
              required
              className="min-w-60 flex-1"
            />
            <Button type="submit" variant="secondary" disabled={rename.isPending}>
              {t('common.save')}
            </Button>
          </form>
          <div className="mt-2">
            {rename.isSuccess && <Status>{t('common.saved')}</Status>}
            <ErrorAlert error={rename.error} />
          </div>
        </Card>
      )}

      <Card title={t('tenant.apps.title')} description={t('tenant.apps.help')}>
        {apps.isPending && <Status>{t('common.loading')}</Status>}
        {apps.isError && <ErrorAlert error={apps.error} />}
        {apps.data && apps.data.length === 0 && <EmptyState>{t('tenant.apps.empty')}</EmptyState>}
        {apps.data && apps.data.length > 0 && (
          <ul className="grid gap-3 sm:grid-cols-2">
            {apps.data.map((app) => (
              <li
                key={app.id}
                className="rounded-md border border-zinc-200 p-4 dark:border-zinc-800"
              >
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <h3 className="font-medium">{app.name}</h3>
                    <p className="text-xs text-zinc-500">
                      {app.slug} · {app.locale}
                    </p>
                  </div>
                  <Badge tone={app.status === 'active' ? 'success' : 'warning'}>
                    {t(`statuses.${app.status}`)}
                  </Badge>
                </div>
                <Link
                  to="/apps/$appId"
                  params={{ appId: app.id }}
                  className="mt-3 inline-block text-sm font-medium text-brand-700 hover:underline dark:text-brand-500"
                >
                  {t('tenant.apps.open')} →
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Dialog open={open} onClose={() => setOpen(false)} title={t('tenant.apps.create')}>
        <form onSubmit={submitCreate} className="space-y-4" aria-busy={create.isPending}>
          <Field
            id="app-name"
            name="name"
            label={t('tenant.apps.name')}
            required
            maxLength={100}
            error={createError?.field('name')}
          />
          <Field
            id="app-slug"
            name="slug"
            label={`${t('tenant.apps.slug')} (${t('common.optional')})`}
            pattern="^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$"
            hint={t('tenant.apps.slugHint')}
            error={createError?.field('slug')}
          />
          <SelectField
            id="app-locale"
            name="locale"
            label={t('tenant.apps.locale')}
            defaultValue="zh-TW"
          >
            {LOCALES.map((l) => (
              <option key={l} value={l}>
                {l}
              </option>
            ))}
          </SelectField>
          <ErrorAlert error={create.error} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {t('common.create')}
            </Button>
          </div>
        </form>
      </Dialog>
      <Input type="hidden" name="tenant" value={tenantId} readOnly className="hidden" />
    </div>
  );
}
