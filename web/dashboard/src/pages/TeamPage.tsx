import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { invitationsQuery, keys, teamQuery } from '../api/queries';
import type { Invitation, InvitationCreate, Role, TeamMember } from '../api/types';
import { useMe } from '../auth/session';
import { useDeclareTenant } from '../auth/tenant';
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
  const { tenantId = '' } = useParams({ strict: false });
  useDeclareTenant(tenantId);
  const me = useMe();
  return <TeamView tenantId={tenantId} role={roleIn(me, tenantId)} meUserId={me?.user.id} />;
}

/** Team members with role management, plus invitations for new people. */
export function TeamView({
  tenantId,
  role,
  meUserId,
}: {
  tenantId: string;
  role: Role | null;
  meUserId: string | undefined;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const team = useQuery(teamQuery(tenantId));
  const [removing, setRemoving] = useState<TeamMember | null>(null);
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.team(tenantId) });

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

  const grantable = grantableRoles(role);
  const canEdit = (member: TeamMember) =>
    role === 'owner' || (role === 'admin' && member.role !== 'owner');
  const [inviting, setInviting] = useState(false);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('team.title')}
        description={t('team.help')}
        actions={
          canManage(role) && <Button onClick={() => setInviting(true)}>{t('team.invite')}</Button>
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
                const isMe = member.user_id === meUserId;
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

      {canManage(role) && (
        <Invitations
          tenantId={tenantId}
          grantable={grantable}
          inviting={inviting}
          onInvitingChange={setInviting}
        />
      )}

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

/** Pending invitations, the invite dialog and the show-once link. */
function Invitations({
  tenantId,
  grantable,
  inviting,
  onInvitingChange,
}: {
  tenantId: string;
  grantable: Role[];
  inviting: boolean;
  onInvitingChange: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const invitations = useQuery(invitationsQuery(tenantId));
  const [created, setCreated] = useState<{ email: string; url: string } | null>(null);
  const [revoking, setRevoking] = useState<Invitation | null>(null);
  const invalidate = () => void qc.invalidateQueries({ queryKey: keys.invitations(tenantId) });

  const create = useIdempotentMutation(
    async (body: InvitationCreate, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/tenants/{tenantId}/invitations', {
          params: { path: { tenantId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: (result) => {
        invalidate();
        onInvitingChange(false);
        setCreated({ email: result.email, url: result.invite_url });
      },
    },
  );

  const revoke = useMutation({
    mutationFn: async (invitationId: string) =>
      unwrap(
        await api.DELETE('/v1/dashboard/invitations/{invitationId}', {
          params: { path: { invitationId } },
        }),
      ),
    onSuccess: () => {
      invalidate();
      setRevoking(null);
    },
  });

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    create.mutate({
      email: String(fd.get('email') ?? '').trim(),
      role: String(fd.get('role') ?? 'staff') as Role,
    });
  }

  const err = asApiError(create.error);

  return (
    <Card title={t('team.pendingTitle')}>
      {invitations.isPending && <Status>{t('common.loading')}</Status>}
      {invitations.isError && <ErrorAlert error={invitations.error} />}
      {invitations.data && invitations.data.length === 0 && (
        <EmptyState>{t('team.pendingEmpty')}</EmptyState>
      )}
      {invitations.data && invitations.data.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>{t('team.email')}</Th>
              <Th>{t('team.role')}</Th>
              <Th>{t('team.expires')}</Th>
              <Th className="text-right">{t('common.actions')}</Th>
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {invitations.data.map((inv) => (
              <tr key={inv.id}>
                <Td className="font-medium">{inv.email}</Td>
                <Td>
                  <Badge>{t(`roles.${inv.role}`)}</Badge>
                </Td>
                <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                  {formatDateTime(inv.expires_at)}
                </Td>
                <Td className="text-right">
                  <Button variant="ghost" onClick={() => setRevoking(inv)}>
                    {t('team.revoke')}
                  </Button>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      <Dialog open={inviting} onClose={() => onInvitingChange(false)} title={t('team.invite')}>
        <form onSubmit={submit} className="space-y-4" aria-busy={create.isPending}>
          <Field
            id="invite-email"
            name="email"
            type="email"
            label={t('team.email')}
            required
            error={err?.field('email')}
          />
          <SelectField
            id="invite-role"
            name="role"
            label={t('team.role')}
            defaultValue="staff"
            error={err?.field('role')}
          >
            {grantable.map((r) => (
              <option key={r} value={r}>
                {t(`roles.${r}`)}
              </option>
            ))}
          </SelectField>
          <ErrorAlert error={create.error} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => onInvitingChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {t('team.invite')}
            </Button>
          </div>
        </form>
      </Dialog>

      <ShowOnceDialog
        open={created !== null}
        title={t('team.inviteLinkTitle')}
        help={t('team.inviteLinkHelp', { email: created?.email ?? '' })}
        value={created?.url ?? ''}
        onClose={() => setCreated(null)}
      />

      <ConfirmDialog
        open={revoking !== null}
        title={t('team.revoke')}
        body={t('team.revokeConfirm', { email: revoking?.email ?? '' })}
        confirmLabel={t('team.revoke')}
        pending={revoke.isPending}
        error={revoke.error}
        onCancel={() => {
          revoke.reset();
          setRevoking(null);
        }}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </Card>
  );
}
