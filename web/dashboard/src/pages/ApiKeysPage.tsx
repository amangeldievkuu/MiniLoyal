import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { apiKeysQuery, keys } from '../api/queries';
import {
  API_KEY_SCOPES,
  type ApiKey,
  type ApiKeyCreate,
  type ApiKeyScope,
  type Role,
} from '../api/types';
import { AppHeader } from '../components/AppHeader';
import {
  ConfirmDialog,
  EmptyState,
  ErrorAlert,
  PageHeader,
  ReadOnlyNotice,
  ShowOnceDialog,
} from '../components/feedback';
import { Badge, Button, Card, Dialog, Field, Status, Table, Td, Th } from '../components/ui';
import { formatDateTime } from '../lib/format';
import { canManage } from '../lib/roles';

export function ApiKeysPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>{(_app, role) => <ApiKeys appId={appId} role={role} />}</AppHeader>
  );
}

function ApiKeys({ appId, role }: { appId: string; role: Role | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const list = useQuery(apiKeysQuery(appId));
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<ApiKey | null>(null);
  const [created, setCreated] = useState<string | null>(null);
  const editable = canManage(role);
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.apiKeys(appId) });

  const create = useIdempotentMutation(
    async (body: ApiKeyCreate, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/apps/{appId}/api-keys', {
          params: { path: { appId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: (result) => {
        invalidate();
        setCreating(false);
        setCreated(result.key);
      },
    },
  );

  const revoke = useMutation({
    mutationFn: async (keyId: string) =>
      unwrap(await api.DELETE('/v1/dashboard/api-keys/{keyId}', { params: { path: { keyId } } })),
    onSuccess: () => {
      invalidate();
      setRevoking(null);
    },
  });

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    create.mutate({
      name: String(fd.get('name') ?? '').trim(),
      scopes: fd.getAll('scopes').map(String) as ApiKeyScope[],
    });
  }

  const err = asApiError(create.error);

  return (
    <div>
      <PageHeader
        title={t('apiKeys.title')}
        description={t('apiKeys.help')}
        actions={
          editable && <Button onClick={() => setCreating(true)}>{t('apiKeys.create')}</Button>
        }
      />
      {!editable && <ReadOnlyNotice />}
      <Card>
        {list.isPending && <Status>{t('common.loading')}</Status>}
        {list.isError && <ErrorAlert error={list.error} />}
        {list.data && list.data.length === 0 && <EmptyState>{t('apiKeys.empty')}</EmptyState>}
        {list.data && list.data.length > 0 && (
          <Table>
            <thead>
              <tr>
                <Th>{t('apiKeys.name')}</Th>
                <Th>{t('apiKeys.prefix')}</Th>
                <Th>{t('apiKeys.scopes')}</Th>
                <Th>{t('apiKeys.lastUsed')}</Th>
                <Th>{t('common.createdAt')}</Th>
                {editable && <Th className="text-right">{t('common.actions')}</Th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {list.data.map((key) => (
                <tr key={key.id}>
                  <Td className="font-medium">{key.name}</Td>
                  <Td className="font-mono text-xs">{key.prefix}…</Td>
                  <Td>
                    <div className="flex flex-wrap gap-1">
                      {key.scopes.map((s) => (
                        <Badge key={s}>{s}</Badge>
                      ))}
                    </div>
                  </Td>
                  <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                    {key.last_used_at ? formatDateTime(key.last_used_at) : t('apiKeys.never')}
                  </Td>
                  <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                    {formatDateTime(key.created_at)}
                  </Td>
                  {editable && (
                    <Td className="text-right">
                      <Button variant="ghost" onClick={() => setRevoking(key)}>
                        {t('apiKeys.revoke')}
                      </Button>
                    </Td>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <Dialog open={creating} onClose={() => setCreating(false)} title={t('apiKeys.create')}>
        <form onSubmit={submit} className="space-y-4" aria-busy={create.isPending}>
          <Field
            id="key-name"
            name="name"
            label={t('apiKeys.name')}
            required
            maxLength={100}
            error={err?.field('name')}
          />
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t('apiKeys.scopes')}</legend>
            {API_KEY_SCOPES.map((scope) => (
              <label key={scope} className="flex items-center gap-2 text-sm">
                <input type="checkbox" name="scopes" value={scope} />
                <span className="font-mono text-xs">{scope}</span>
                <span className="text-zinc-600 dark:text-zinc-400">
                  {t(`apiKeys.scopeLabels.${scope}`)}
                </span>
              </label>
            ))}
            {err?.field('scopes') && (
              <p className="text-xs text-red-600 dark:text-red-400">{err.field('scopes')}</p>
            )}
          </fieldset>
          <ErrorAlert error={create.error} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setCreating(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {t('common.create')}
            </Button>
          </div>
        </form>
      </Dialog>

      <ShowOnceDialog
        open={created !== null}
        title={t('apiKeys.keyTitle')}
        help={t('apiKeys.keyHelp')}
        value={created ?? ''}
        onClose={() => setCreated(null)}
      />

      <ConfirmDialog
        open={revoking !== null}
        title={t('apiKeys.revoke')}
        body={t('apiKeys.revokeConfirm', { name: revoking?.name ?? '' })}
        confirmLabel={t('apiKeys.revoke')}
        pending={revoke.isPending}
        error={revoke.error}
        onCancel={() => {
          revoke.reset();
          setRevoking(null);
        }}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </div>
  );
}
