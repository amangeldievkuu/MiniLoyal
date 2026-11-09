import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { keys, teamQuery } from '../api/queries';
import type { Role, TeamMember, TeamMemberAdd } from '../api/types';
import { useMe } from '../auth/session';
import { useDeclareTenant } from '../auth/tenant';
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
  Select,
  SelectField,
  Status,
  Table,
  Td,
  Th,
} from '../components/ui';
import { formatDateTime } from '../lib/format';
import { canManage, grantableRoles, roleIn } from '../lib/roles';

export function TeamPage() {
  const { t } = useTranslation();
  const { tenantId = '' } = useParams({ strict: false });
  useDeclareTenant(tenantId);
  const me = useMe();
  const role = roleIn(me, tenantId);
  const qc = useQueryClient();
  const team = useQuery(teamQuery(tenantId));
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<TeamMember | null>(null);
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.team(tenantId) });

  const add = useIdempotentMutation(
    async (body: TeamMemberAdd, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/tenants/{tenantId}/team', {
          params: { path: { tenantId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: () => {
        invalidate();
        setAdding(false);
      },
    },
  );

  const changeRole = useIdempotentMutation(
    async ({ userId, role }: { userId: string; role: Role }, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/tenants/{tenantId}/team/{userId}', {
          params: { path: { tenantId, userId }, header: { 'Idempotency-Key': key } },
          body: { role },
        }),
      ),
    { onSuccess: invalidate },
  );

  const remove = useMutation({
    mutationFn: async (userId: string) =>
      unwrap(
        await api.DELETE('/v1/dashboard/tenants/{tenantId}/team/{userId}', {
          params: { path: { tenantId, userId } },
        }),
      ),
    onSuccess: () => {
      invalidate();
      setRemoving(null);
    },
  });

  function submitAdd(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    add.mutate({
      email: String(fd.get('email') ?? '').trim(),
      role: String(fd.get('role') ?? 'staff') as Role,
    });
  }

  const grantable = grantableRoles(role);
  const canEdit = (member: TeamMember) =>
    role === 'owner' || (role === 'admin' && member.role !== 'owner');
  const addError = asApiError(add.error);

  return (
    <div>
      <PageHeader
        title={t('team.title')}
        description={t('team.help')}
        actions={
          canManage(role) && <Button onClick={() => setAdding(true)}>{t('team.add')}</Button>
        }
      />
      {role === 'staff' && <ReadOnlyNotice />}
      <Card>
        {team.isPending && <Status>{t('common.loading')}</Status>}
        {team.isError && <ErrorAlert error={team.error} />}
        <ErrorAlert error={changeRole.error} />
        {team.data && team.data.length === 0 && <EmptyState>{t('team.empty')}</EmptyState>}
        {team.data && team.data.length > 0 && (
          <Table>
            <thead>
              <tr>
                <Th>{t('team.email')}</Th>
                <Th>{t('team.role')}</Th>
                <Th>{t('common.createdAt')}</Th>
                {canManage(role) && <Th className="text-right">{t('common.actions')}</Th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {team.data.map((member) => {
                const isMe = member.user_id === me?.user.id;
                return (
                  <tr key={member.user_id}>
                    <Td>
                      <div className="font-medium">
                        {member.email}{' '}
                        {isMe && <span className="text-zinc-500">{t('team.you')}</span>}
                      </div>
                      {member.display_name && (
                        <div className="text-xs text-zinc-500">{member.display_name}</div>
                      )}
                    </Td>
                    <Td>
                      {canManage(role) && canEdit(member) && !isMe ? (
                        <Select
                          aria-label={t('team.role')}
                          className="w-auto"
                          value={member.role}
                          disabled={changeRole.isPending}
                          onChange={(e) =>
                            changeRole.mutate({
                              userId: member.user_id,
                              role: e.target.value as Role,
                            })
                          }
                        >
                          {grantable.map((r) => (
                            <option key={r} value={r}>
                              {t(`roles.${r}`)}
                            </option>
                          ))}
                        </Select>
                      ) : (
                        <Badge>{t(`roles.${member.role}`)}</Badge>
                      )}
                    </Td>
                    <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                      {formatDateTime(member.created_at)}
                    </Td>
                    {canManage(role) && (
                      <Td className="text-right">
                        {canEdit(member) && !isMe && (
                          <Button variant="ghost" onClick={() => setRemoving(member)}>
                            {t('common.remove')}
                          </Button>
                        )}
                      </Td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </Table>
        )}
      </Card>

      <Dialog open={adding} onClose={() => setAdding(false)} title={t('team.add')}>
        <form onSubmit={submitAdd} className="space-y-4" aria-busy={add.isPending}>
          <Field
            id="team-email"
            name="email"
            type="email"
            label={t('team.email')}
            required
            error={addError?.field('email')}
          />
          <SelectField id="team-role" name="role" label={t('team.role')} defaultValue="staff">
            {grantable.map((r) => (
              <option key={r} value={r}>
                {t(`roles.${r}`)}
              </option>
            ))}
          </SelectField>
          <ErrorAlert error={add.error} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setAdding(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={add.isPending}>
              {t('team.add')}
            </Button>
          </div>
        </form>
      </Dialog>

      <ConfirmDialog
        open={removing !== null}
        title={t('team.remove')}
        body={t('team.removeConfirm', { email: removing?.email ?? '' })}
        confirmLabel={t('common.remove')}
        pending={remove.isPending}
        error={remove.error}
        onCancel={() => {
          remove.reset();
          setRemoving(null);
        }}
        onConfirm={() => removing && remove.mutate(removing.user_id)}
      />
    </div>
  );
}
