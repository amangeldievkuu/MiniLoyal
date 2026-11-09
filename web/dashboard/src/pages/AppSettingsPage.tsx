import { useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { keys } from '../api/queries';
import {
  APP_STATUSES,
  LOCALES,
  type App,
  type AppStatus,
  type AppUpdate,
  type Role,
} from '../api/types';
import { AppHeader } from '../components/AppHeader';
import { ErrorAlert, ReadOnlyNotice } from '../components/feedback';
import { Badge, Button, Card, Field, SelectField, Status } from '../components/ui';
import { canManage } from '../lib/roles';

export function AppSettingsPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>{(app, role) => <SettingsForm app={app} role={role} />}</AppHeader>
  );
}

function SettingsForm({ app, role }: { app: App; role: Role | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [clearSecret, setClearSecret] = useState(false);
  const editable = canManage(role);

  const update = useIdempotentMutation(
    async (body: AppUpdate, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/apps/{appId}', {
          params: { path: { appId: app.id }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: keys.app(app.id) });
        void qc.invalidateQueries({ queryKey: keys.apps(app.tenant_id) });
        setClearSecret(false);
      },
    },
  );

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const str = (name: string) => String(fd.get(name) ?? '').trim();
    const body: AppUpdate = {};
    if (str('name') !== app.name) body.name = str('name');
    if (str('locale') !== app.locale) body.locale = str('locale');
    if (str('status') !== app.status) body.status = str('status') as AppStatus;
    if ((str('oa_channel_id') || null) !== app.oa_channel_id)
      body.oa_channel_id = str('oa_channel_id') || null;
    if ((str('oa_basic_id') || null) !== app.oa_basic_id)
      body.oa_basic_id = str('oa_basic_id') || null;
    const secret = String(fd.get('oa_channel_secret') ?? '');
    if (clearSecret) body.oa_channel_secret = '';
    else if (secret) body.oa_channel_secret = secret;
    update.mutate(body);
    e.currentTarget.reset();
  }

  const err = asApiError(update.error);

  return (
    <form onSubmit={submit} aria-busy={update.isPending} className="space-y-6">
      {!editable && <ReadOnlyNotice />}
      <Card title={t('appPage.settings.title')}>
        <fieldset disabled={!editable} className="grid gap-4 sm:grid-cols-2">
          <Field
            id="name"
            name="name"
            label={t('appPage.settings.name')}
            defaultValue={app.name}
            required
            maxLength={100}
            error={err?.field('name')}
          />
          <SelectField
            id="locale"
            name="locale"
            label={t('appPage.settings.locale')}
            defaultValue={app.locale}
          >
            {LOCALES.map((l) => (
              <option key={l} value={l}>
                {l}
              </option>
            ))}
          </SelectField>
          <SelectField
            id="status"
            name="status"
            label={t('appPage.settings.status')}
            defaultValue={app.status}
          >
            {APP_STATUSES.map((s) => (
              <option key={s} value={s}>
                {t(`statuses.${s}`)}
              </option>
            ))}
          </SelectField>
        </fieldset>
      </Card>

      <Card
        title={t('appPage.settings.oaTitle')}
        description={t('appPage.settings.oaHelp')}
        actions={
          <Badge tone={app.oa_channel_secret_set ? 'success' : 'warning'}>
            {app.oa_channel_secret_set
              ? t('appPage.settings.oaSecretSet')
              : t('appPage.settings.oaSecretNotSet')}
          </Badge>
        }
      >
        <fieldset disabled={!editable} className="grid gap-4 sm:grid-cols-2">
          <Field
            id="oa_channel_id"
            name="oa_channel_id"
            label={t('appPage.settings.oaChannelId')}
            defaultValue={app.oa_channel_id ?? ''}
            inputMode="numeric"
            maxLength={32}
            error={err?.field('oa_channel_id')}
          />
          <Field
            id="oa_basic_id"
            name="oa_basic_id"
            label={t('appPage.settings.oaBasicId')}
            defaultValue={app.oa_basic_id ?? ''}
            maxLength={32}
            error={err?.field('oa_basic_id')}
          />
          <Field
            id="oa_channel_secret"
            name="oa_channel_secret"
            type="password"
            autoComplete="off"
            label={t('appPage.settings.oaSecret')}
            placeholder={app.oa_channel_secret_set ? '••••••••' : ''}
            hint={t('appPage.settings.oaSecretHint')}
            maxLength={128}
            disabled={clearSecret}
            error={err?.field('oa_channel_secret')}
          />
          {app.oa_channel_secret_set && (
            <label className="flex items-center gap-2 self-end text-sm">
              <input
                type="checkbox"
                checked={clearSecret}
                onChange={(e) => setClearSecret(e.target.checked)}
              />
              {t('appPage.settings.clearSecret')}
            </label>
          )}
        </fieldset>
      </Card>

      {editable && (
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={update.isPending}>
            {update.isPending ? t('common.working') : t('common.save')}
          </Button>
          {update.isSuccess && <Status>{t('common.saved')}</Status>}
        </div>
      )}
      <ErrorAlert error={update.error} />
    </form>
  );
}
