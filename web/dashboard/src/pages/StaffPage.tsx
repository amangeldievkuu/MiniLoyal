import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { keys, staffQuery, storesQuery } from '../api/queries';
import {
  APP_STATUSES,
  STAFF_ROLES,
  type AppStatus,
  type Role,
  type Staff,
  type StaffCreate,
  type StaffRole,
  type StaffUpdate,
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

export function StaffPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>{(_app, role) => <StaffList appId={appId} role={role} />}</AppHeader>
  );
}

type Editing = { kind: 'create' } | { kind: 'edit'; staff: Staff } | null;

function StaffList({ appId, role }: { appId: string; role: Role | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const staff = useQuery(staffQuery(appId));
  const stores = useQuery(storesQuery(appId));
  const [editing, setEditing] = useState<Editing>(null);
  const [deleting, setDeleting] = useState<Staff | null>(null);
  const [inviteCode, setInviteCode] = useState<string | null>(null);
  const editable = canManage(role);
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.staff(appId) });
  const storeName = (id: string | null | undefined) =>
    stores.data?.find((s) => s.id === id)?.name ?? t('staff.noStore');

  const create = useIdempotentMutation(
    async (body: StaffCreate, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/apps/{appId}/staff', {
          params: { path: { appId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: (created) => {
        invalidate();
        setEditing(null);
        setInviteCode(created.invite_code);
      },
    },
  );

  const update = useIdempotentMutation(
    async ({ staffId, body }: { staffId: string; body: StaffUpdate }, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/staff/{staffId}', {
          params: { path: { staffId }, header: { 'Idempotency-Key': key } },
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
    mutationFn: async (staffId: string) =>
      unwrap(await api.DELETE('/v1/dashboard/staff/{staffId}', { params: { path: { staffId } } })),
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
    const storeId = str('store_id');
    if (editing.kind === 'create') {
      create.mutate({
        display_name: str('display_name'),
        role: str('role') as StaffRole,
        ...(storeId ? { store_id: storeId } : {}),
      });
    } else {
      update.mutate({
        staffId: editing.staff.id,
        body: {
          display_name: str('display_name'),
          role: str('role') as StaffRole,
          store_id: storeId || null,
          status: str('status') as AppStatus,
        },
      });
    }
  }

  const active = editing?.kind === 'create' ? create : update;
  const err = asApiError(active.error);

  if (staff.isPending) return <Status>{t('common.loading')}</Status>;
  if (staff.isError) return <ErrorAlert error={staff.error} />;

  return (
    <div>
      <PageHeader
        title={t('staff.title')}
        description={t('staff.help')}
        actions={
          editable && (
            <Button onClick={() => setEditing({ kind: 'create' })}>{t('staff.create')}</Button>
          )
        }
      />
      {!editable && <ReadOnlyNotice />}
      <Card>
        {staff.data.length === 0 ? (
          <EmptyState>{t('staff.empty')}</EmptyState>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t('staff.displayName')}</Th>
                <Th>{t('staff.store')}</Th>
                <Th>{t('staff.role')}</Th>
                <Th>{t('staff.line')}</Th>
                <Th>{t('common.status')}</Th>
                {editable && <Th className="text-right">{t('common.actions')}</Th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {staff.data.map((member) => (
                <tr key={member.id}>
                  <Td className="font-medium">{member.display_name}</Td>
                  <Td>{storeName(member.store_id)}</Td>
                  <Td>{t(`staff.roles.${member.role}`)}</Td>
                  <Td>
                    <Badge tone={member.line_linked ? 'success' : 'neutral'}>
                      {member.line_linked ? t('staff.linked') : t('staff.notLinked')}
                    </Badge>
                  </Td>
                  <Td>
                    <Badge tone={member.status === 'active' ? 'success' : 'warning'}>
                      {t(`statuses.${member.status}`)}
                    </Badge>
                  </Td>
                  {editable && (
                    <Td className="text-right whitespace-nowrap">
                      <Button
                        variant="ghost"
                        onClick={() => setEditing({ kind: 'edit', staff: member })}
                      >
                        {t('common.edit')}
                      </Button>
                      <Button variant="ghost" onClick={() => setDeleting(member)}>
                        {t('common.remove')}
                      </Button>
                    </Td>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <Dialog
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing?.kind === 'edit' ? t('staff.edit') : t('staff.create')}
      >
        {editing && (
          <form onSubmit={submit} className="space-y-4" aria-busy={active.isPending}>
            <Field
              id="staff-name"
              name="display_name"
              label={t('staff.displayName')}
              defaultValue={editing.kind === 'edit' ? editing.staff.display_name : ''}
              required
              maxLength={100}
              error={err?.field('display_name')}
            />
            <SelectField
              id="staff-store"
              name="store_id"
              label={t('staff.store')}
              defaultValue={editing.kind === 'edit' ? (editing.staff.store_id ?? '') : ''}
              error={err?.field('store_id')}
            >
              <option value="">{t('staff.noStore')}</option>
              {stores.data?.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </SelectField>
            <SelectField
              id="staff-role"
              name="role"
              label={t('staff.role')}
              defaultValue={editing.kind === 'edit' ? editing.staff.role : 'cashier'}
            >
              {STAFF_ROLES.map((r) => (
                <option key={r} value={r}>
                  {t(`staff.roles.${r}`)}
                </option>
              ))}
            </SelectField>
            {editing.kind === 'edit' && (
              <SelectField
                id="staff-status"
                name="status"
                label={t('common.status')}
                defaultValue={editing.staff.status}
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

      <ShowOnceDialog
        open={inviteCode !== null}
        title={t('staff.inviteTitle')}
        help={t('staff.inviteHelp')}
        value={inviteCode ?? ''}
        onClose={() => setInviteCode(null)}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={t('common.remove')}
        body={t('staff.deleteConfirm', { name: deleting?.display_name ?? '' })}
        confirmLabel={t('common.remove')}
        pending={remove.isPending}
        error={remove.error}
        onCancel={() => {
          remove.reset();
          setDeleting(null);
        }}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </div>
  );
}
