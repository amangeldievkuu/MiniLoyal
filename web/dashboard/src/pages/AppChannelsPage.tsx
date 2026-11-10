import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { channelsQuery, keys } from '../api/queries';
import {
  CHANNEL_ENVIRONMENTS,
  type AppChannel,
  type AppChannelPut,
  type ChannelEnvironment,
  type Role,
} from '../api/types';
import { AppHeader } from '../components/AppHeader';
import { ConfirmDialog, ErrorAlert, ReadOnlyNotice } from '../components/feedback';
import { Badge, Button, Card, Field, Status } from '../components/ui';
import { canManage } from '../lib/roles';

export function AppChannelsPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>{(_app, role) => <Channels appId={appId} role={role} />}</AppHeader>
  );
}

function Channels({ appId, role }: { appId: string; role: Role | null }) {
  const { t } = useTranslation();
  const channels = useQuery(channelsQuery(appId));
  const byEnv = new Map(channels.data?.map((c) => [c.environment, c]) ?? []);

  return (
    <div className="space-y-4">
      {!canManage(role) && <ReadOnlyNotice />}
      <p className="text-sm text-zinc-600 dark:text-zinc-400">{t('channels.help')}</p>
      {channels.isPending && <Status>{t('common.loading')}</Status>}
      {channels.isError && <ErrorAlert error={channels.error} />}
      {channels.data &&
        CHANNEL_ENVIRONMENTS.map((env) => (
          <ChannelCard
            key={env}
            appId={appId}
            environment={env}
            channel={byEnv.get(env)}
            editable={canManage(role)}
          />
        ))}
    </div>
  );
}

function ChannelCard({
  appId,
  environment,
  channel,
  editable,
}: {
  appId: string;
  environment: ChannelEnvironment;
  channel: AppChannel | undefined;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [confirmRemove, setConfirmRemove] = useState(false);
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.channels(appId) });

  const save = useIdempotentMutation(
    async (body: AppChannelPut, key: string) =>
      unwrap(
        await api.PUT('/v1/dashboard/apps/{appId}/channels/{environment}', {
          params: { path: { appId, environment }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    { onSuccess: invalidate },
  );

  const remove = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.DELETE('/v1/dashboard/apps/{appId}/channels/{environment}', {
          params: { path: { appId, environment } },
        }),
      ),
    onSuccess: () => {
      invalidate();
      setConfirmRemove(false);
    },
  });

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    // The secret is required: the API proves control of the channel with it.
    save.mutate({
      channel_id: String(fd.get('channel_id') ?? '').trim(),
      liff_id: String(fd.get('liff_id') ?? '').trim(),
      channel_secret: String(fd.get('channel_secret') ?? ''),
    });
    const secretInput = e.currentTarget.elements.namedItem('channel_secret');
    if (secretInput instanceof HTMLInputElement) secretInput.value = '';
  }

  const err = asApiError(save.error);
  const envLabel = t(`channels.env.${environment}`);

  return (
    <Card
      title={envLabel}
      actions={
        channel ? (
          <>
            <Badge tone={channel.channel_secret_set ? 'success' : 'neutral'}>
              {channel.channel_secret_set ? t('channels.secretSet') : t('common.set')}
            </Badge>
            <Badge tone={channel.verified_at ? 'success' : 'warning'}>
              {channel.verified_at ? t('channels.verified') : t('channels.notVerified')}
            </Badge>
          </>
        ) : (
          <Badge tone="warning">{t('channels.notConfigured')}</Badge>
        )
      }
    >
      {environment === 'staff' && (
        <p className="mb-3 text-sm text-zinc-600 dark:text-zinc-400">{t('channels.staffHelp')}</p>
      )}
      <form onSubmit={submit} aria-busy={save.isPending}>
        <fieldset disabled={!editable} className="grid gap-4 sm:grid-cols-3">
          <Field
            id={`${environment}-channel-id`}
            name="channel_id"
            label={t('channels.channelId')}
            defaultValue={channel?.channel_id ?? ''}
            required
            maxLength={32}
            error={err?.field('channel_id')}
          />
          <Field
            id={`${environment}-liff-id`}
            name="liff_id"
            label={t('channels.liffId')}
            defaultValue={channel?.liff_id ?? ''}
            required
            maxLength={64}
            error={err?.field('liff_id')}
          />
          <Field
            id={`${environment}-secret`}
            name="channel_secret"
            type="password"
            autoComplete="off"
            label={t('channels.secret')}
            placeholder={channel?.channel_secret_set ? '••••••••' : ''}
            hint={t('channels.secretHint')}
            required
            minLength={1}
            maxLength={128}
            error={err?.field('channel_secret')}
          />
        </fieldset>
        {editable && (
          <div className="mt-4 flex flex-wrap items-center gap-2">
            <Button type="submit" disabled={save.isPending}>
              {t('common.save')}
            </Button>
            {channel && (
              <Button variant="ghost" onClick={() => setConfirmRemove(true)}>
                {t('channels.remove')}
              </Button>
            )}
            {save.isSuccess && <Status>{t('common.saved')}</Status>}
          </div>
        )}
        <div className="mt-3">
          <ErrorAlert error={save.error} />
        </div>
      </form>
      <ConfirmDialog
        open={confirmRemove}
        title={t('channels.remove')}
        body={t('channels.removeConfirm', { environment: envLabel })}
        confirmLabel={t('common.remove')}
        pending={remove.isPending}
        error={remove.error}
        onCancel={() => {
          remove.reset();
          setConfirmRemove(false);
        }}
        onConfirm={() => remove.mutate()}
      />
    </Card>
  );
}
