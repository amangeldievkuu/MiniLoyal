import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { keys, storesQuery } from '../api/queries';
import {
  APP_STATUSES,
  type AppStatus,
  type Role,
  type Store,
  type StoreCreate,
  type StoreUpdate,
} from '../api/types';
import { AppHeader } from '../components/AppHeader';
import {
  ConfirmDialog,
  EmptyState,
  ErrorAlert,
  PageHeader,
  ReadOnlyNotice,
} from '../components/feedback';
import {
  Badge,
  Button,
  Card,
  Dialog,
  Field,
  SelectField,
  Status,
  Table,
  Td,
  Th,
} from '../components/ui';
import { canManage } from '../lib/roles';

export function StoresPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>
      {(_app, role) => <StoresContainer appId={appId} role={role} />}
    </AppHeader>
  );
}

/** Presentational list; actions are hidden for roles that cannot manage. */
export function StoresView({
  stores,
  role,
  onCreate,
  onEdit,
  onDelete,
}: {
  stores: Store[];
  role: Role | null;
  onCreate: () => void;
  onEdit: (store: Store) => void;
  onDelete: (store: Store) => void;
}) {
  const { t } = useTranslation();
  const editable = canManage(role);
  return (
    <div>
      <PageHeader
        title={t('stores.title')}
        description={t('stores.help')}
        actions={editable && <Button onClick={onCreate}>{t('stores.create')}</Button>}
      />
      {!editable && <ReadOnlyNotice />}
      <Card>
        {stores.length === 0 ? (
          <EmptyState>{t('stores.empty')}</EmptyState>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t('stores.name')}</Th>
                <Th>{t('stores.code')}</Th>
                <Th>{t('stores.address')}</Th>
                <Th>{t('common.status')}</Th>
                {editable && <Th className="text-right">{t('common.actions')}</Th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {stores.map((store) => (
                <tr key={store.id}>
                  <Td className="font-medium">{store.name}</Td>
                  <Td className="font-mono text-xs">{store.code ?? ''}</Td>
                  <Td className="text-zinc-600 dark:text-zinc-400">{store.address ?? ''}</Td>
                  <Td>
                    <Badge tone={store.status === 'active' ? 'success' : 'warning'}>
                      {t(`statuses.${store.status}`)}
                    </Badge>
                  </Td>
                  {editable && (
                    <Td className="text-right whitespace-nowrap">
                      <Button variant="ghost" onClick={() => onEdit(store)}>
                        {t('common.edit')}
                      </Button>
                      <Button variant="ghost" onClick={() => onDelete(store)}>
                        {t('common.delete')}
                      </Button>
                    </Td>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </div>
  );
}

type Editing = { kind: 'create' } | { kind: 'edit'; store: Store } | null;

function StoresContainer({ appId, role }: { appId: string; role: Role | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const stores = useQuery(storesQuery(appId));
  const [editing, setEditing] = useState<Editing>(null);
  const [deleting, setDeleting] = useState<Store | null>(null);
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.stores(appId) });

  const create = useIdempotentMutation(
    async (body: StoreCreate, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/apps/{appId}/stores', {
          params: { path: { appId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: () => {
        invalidate();
        setEditing(null);
      },
    },
  );

  const update = useIdempotentMutation(
    async ({ storeId, body }: { storeId: string; body: StoreUpdate }, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/stores/{storeId}', {
          params: { path: { storeId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: () => {
        invalidate();
        setEditing(null);
      },
    },
  );

  const remove = useMutation({
    mutationFn: async (storeId: string) =>
      unwrap(await api.DELETE('/v1/dashboard/stores/{storeId}', { params: { path: { storeId } } })),
    onSuccess: () => {
      invalidate();
      setDeleting(null);
    },
  });

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!editing) return;
    const fd = new FormData(e.currentTarget);
    const str = (name: string) => String(fd.get(name) ?? '').trim();
    if (editing.kind === 'create') {
      create.mutate({
        name: str('name'),
        ...(str('code') ? { code: str('code') } : {}),
        ...(str('address') ? { address: str('address') } : {}),
      });
    } else {
      update.mutate({
        storeId: editing.store.id,
        body: {
          name: str('name'),
          code: str('code') || null,
          address: str('address') || null,
          status: str('status') as AppStatus,
        },
      });
    }
  }

  const active = editing?.kind === 'create' ? create : update;
  const err = asApiError(active.error);

  if (stores.isPending) return <Status>{t('common.loading')}</Status>;
  if (stores.isError) return <ErrorAlert error={stores.error} />;

  return (
    <>
      <StoresView
        stores={stores.data}
        role={role}
        onCreate={() => setEditing({ kind: 'create' })}
        onEdit={(store) => setEditing({ kind: 'edit', store })}
        onDelete={setDeleting}
      />
      <Dialog
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing?.kind === 'edit' ? t('stores.edit') : t('stores.create')}
      >
        {editing && (
          <form onSubmit={submit} className="space-y-4" aria-busy={active.isPending}>
            <Field
              id="store-name"
              name="name"
              label={t('stores.name')}
              defaultValue={editing.kind === 'edit' ? editing.store.name : ''}
              required
              maxLength={100}
              error={err?.field('name')}
            />
            <Field
              id="store-code"
              name="code"
              label={`${t('stores.code')} (${t('common.optional')})`}
              defaultValue={editing.kind === 'edit' ? (editing.store.code ?? '') : ''}
              maxLength={32}
              error={err?.field('code')}
            />
            <Field
              id="store-address"
              name="address"
              label={`${t('stores.address')} (${t('common.optional')})`}
              defaultValue={editing.kind === 'edit' ? (editing.store.address ?? '') : ''}
              maxLength={300}
              error={err?.field('address')}
            />
            {editing.kind === 'edit' && (
              <SelectField
                id="store-status"
                name="status"
                label={t('common.status')}
                defaultValue={editing.store.status}
              >
                {APP_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {t(`statuses.${s}`)}
                  </option>
                ))}
              </SelectField>
            )}
            <ErrorAlert error={active.error} />
            <div className="flex justify-end gap-2">
              <Button variant="secondary" onClick={() => setEditing(null)}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={active.isPending}>
                {t('common.save')}
              </Button>
            </div>
          </form>
        )}
      </Dialog>
      <ConfirmDialog
        open={deleting !== null}
        title={t('common.delete')}
        body={t('stores.deleteConfirm', { name: deleting?.name ?? '' })}
        confirmLabel={t('common.delete')}
        pending={remove.isPending}
        error={remove.error}
        onCancel={() => {
          remove.reset();
          setDeleting(null);
        }}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </>
  );
}
