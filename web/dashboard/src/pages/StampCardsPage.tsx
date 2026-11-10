import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { useActionMutation } from '../api/actionMutation';
import { api, asApiError, unwrap } from '../api/client';
import { couponTemplatesQuery, stampCardsQuery } from '../api/loyaltyQueries';
import { keys } from '../api/queries';
import { ARCHIVE_STATUSES, type ArchiveStatus, type Role, type StampCard } from '../api/types';
import { AppHeader } from '../components/AppHeader';
import { EmptyState, ErrorAlert, PageHeader, ReadOnlyNotice } from '../components/feedback';
import {
  Alert,
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
import {
  emptyStampDraft,
  toStampCreate,
  toStampDraft,
  toStampUpdate,
  validateStamp,
  type StampDraft,
} from '../features/stamps/stampForm';
import { canManage } from '../lib/roles';

export function StampCardsPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>
      {(_app, role) => <StampCardsView appId={appId} role={role} />}
    </AppHeader>
  );
}

type Editing = { kind: 'create' } | { kind: 'edit'; card: StampCard } | null;

export function StampCardsView({ appId, role }: { appId: string; role: Role | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const editable = canManage(role);
  const [filter, setFilter] = useState<ArchiveStatus | ''>('');
  const [editing, setEditing] = useState<Editing>(null);
  const list = useQuery(stampCardsQuery(appId, filter || undefined));
  const templates = useQuery(couponTemplatesQuery(appId));
  const rewardName = (id: string | null | undefined) =>
    id
      ? (templates.data?.find((x) => x.id === id)?.name ?? id.slice(0, 8))
      : t('stampCards.noReward');

  const setStatus = useActionMutation(
    async ({ id, status }: { id: string; status: ArchiveStatus }, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/stamp-cards/{stampCardId}', {
          params: { path: { stampCardId: id }, header: { 'Idempotency-Key': key } },
          body: { status },
        }),
      ),
    { onSuccess: () => void qc.invalidateQueries({ queryKey: keys.stampCardsRoot(appId) }) },
  );

  return (
    <div>
      <PageHeader
        title={t('stampCards.title')}
        description={t('stampCards.help')}
        actions={
          editable && (
            <Button onClick={() => setEditing({ kind: 'create' })}>{t('stampCards.create')}</Button>
          )
        }
      />
      {!editable && <ReadOnlyNotice />}
      <div className="mb-4 max-w-xs">
        <SelectField
          id="stamp-filter"
          label={t('coupons.filter')}
          value={filter}
          onChange={(e) => setFilter(e.target.value as ArchiveStatus | '')}
        >
          <option value="">{t('common.all')}</option>
          {ARCHIVE_STATUSES.map((s) => (
            <option key={s} value={s}>
              {t(`statuses.${s}`)}
            </option>
          ))}
        </SelectField>
      </div>
      <Card>
        {list.isPending ? (
          <Status>{t('common.loading')}</Status>
        ) : list.isError ? (
          <ErrorAlert error={list.error} />
        ) : list.data.length === 0 ? (
          <EmptyState>{t('stampCards.empty')}</EmptyState>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t('stampCards.name')}</Th>
                <Th className="text-right">{t('stampCards.stampsRequired')}</Th>
                <Th>{t('stampCards.reward')}</Th>
                <Th className="text-right">{t('stampCards.maxPerVisit')}</Th>
                <Th>{t('common.status')}</Th>
                {editable && <Th className="text-right">{t('common.actions')}</Th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {list.data.map((card) => (
                <tr key={card.id}>
                  <Td className="font-medium">{card.name}</Td>
                  <Td className="text-right tabular-nums">{card.stamps_required}</Td>
                  <Td>{rewardName(card.reward_coupon_template_id)}</Td>
                  <Td className="text-right tabular-nums">{card.max_stamps_per_visit}</Td>
                  <Td>
                    <Badge tone={card.status === 'active' ? 'success' : 'warning'}>
                      {t(`statuses.${card.status}`)}
                    </Badge>
                  </Td>
                  {editable && (
                    <Td className="text-right whitespace-nowrap">
                      <Button
                        variant="ghost"
                        aria-label={`${t('common.edit')}: ${card.name}`}
                        onClick={() => setEditing({ kind: 'edit', card })}
                      >
                        {t('common.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        aria-label={`${card.status === 'active' ? t('coupons.archive') : t('coupons.reactivate')}: ${card.name}`}
                        disabled={setStatus.isPending}
                        onClick={() =>
                          setStatus.mutate({
                            id: card.id,
                            status: card.status === 'active' ? 'archived' : 'active',
                          })
                        }
                      >
                        {card.status === 'active' ? t('coupons.archive') : t('coupons.reactivate')}
                      </Button>
                    </Td>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        <div aria-live="polite" className="mt-3">
          <ErrorAlert error={setStatus.error} />
        </div>
      </Card>

      <Dialog
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing?.kind === 'edit' ? t('stampCards.edit') : t('stampCards.create')}
      >
        {editing && (
          <StampCardForm
            key={editing.kind === 'edit' ? editing.card.id : 'create'}
            appId={appId}
            card={editing.kind === 'edit' ? editing.card : null}
            onClose={() => setEditing(null)}
          />
        )}
      </Dialog>
    </div>
  );
}

function StampCardForm({
  appId,
  card,
  onClose,
}: {
  appId: string;
  card: StampCard | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const templates = useQuery(couponTemplatesQuery(appId));
  const [draft, setDraft] = useState<StampDraft>(() =>
    card ? toStampDraft(card) : emptyStampDraft,
  );
  const [submitted, setSubmitted] = useState(false);
  const [inUse, setInUse] = useState(false);
  const errors = validateStamp(draft);

  const done = () => {
    void qc.invalidateQueries({ queryKey: keys.stampCardsRoot(appId) });
    onClose();
  };

  const create = useActionMutation(
    async (body: ReturnType<typeof toStampCreate>, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/apps/{appId}/stamp-cards', {
          params: { path: { appId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    { onSuccess: done },
  );
  const update = useActionMutation(
    async (body: ReturnType<typeof toStampUpdate>, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/stamp-cards/{stampCardId}', {
          params: { path: { stampCardId: card?.id ?? '' }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: done,
      onError: (e) => {
        if (asApiError(e)?.code === 'stamp_card_in_use') {
          setInUse(true);
          // Put the locked value back so the next save does not send it again.
          setDraft((d) => ({ ...d, stamps_required: String(card?.stamps_required ?? '') }));
        }
      },
    },
  );

  const active = card ? update : create;
  const err = asApiError(active.error);
  const handled = err?.code === 'stamp_card_in_use';
  const updateBody = card ? toStampUpdate(draft, card) : null;
  const unchanged = updateBody !== null && Object.keys(updateBody).length === 0;

  // Active templates, plus the current reward when it has since been archived.
  const choices = (templates.data ?? []).filter(
    (x) => x.status === 'active' || x.id === card?.reward_coupon_template_id,
  );

  function set<K extends keyof StampDraft>(field: K, value: StampDraft[K]) {
    setDraft((d) => ({ ...d, [field]: value }));
  }
  const msg = (k: keyof StampDraft) => {
    const key = errors[k];
    return (submitted && key ? t(`stampCards.errors.${key}`) : undefined) ?? err?.field(k);
  };

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setSubmitted(true);
    if (Object.keys(errors).length > 0) return;
    if (card && updateBody) update.mutate(updateBody);
    else create.mutate(toStampCreate(draft));
  }

  return (
    <form onSubmit={submit} noValidate className="space-y-4" aria-busy={active.isPending}>
      <Field
        id="stamp-name"
        name="name"
        label={t('stampCards.name')}
        required
        maxLength={100}
        value={draft.name}
        onChange={(e) => set('name', e.target.value)}
        error={msg('name')}
      />
      <Field
        id="stamp-required"
        name="stamps_required"
        label={t('stampCards.stampsRequired')}
        hint={inUse ? t('stampCards.inUseHint') : t('stampCards.stampsRequiredHint')}
        inputMode="numeric"
        required
        disabled={inUse}
        value={draft.stamps_required}
        onChange={(e) => set('stamps_required', e.target.value)}
        error={msg('stamps_required')}
      />
      <SelectField
        id="stamp-reward"
        name="reward_coupon_template_id"
        label={t('stampCards.reward')}
        hint={t('stampCards.rewardHint')}
        value={draft.reward_coupon_template_id}
        onChange={(e) => set('reward_coupon_template_id', e.target.value)}
        error={msg('reward_coupon_template_id')}
      >
        <option value="">{t('stampCards.noReward')}</option>
        {choices.map((x) => (
          <option key={x.id} value={x.id}>
            {x.name}
          </option>
        ))}
      </SelectField>
      <Field
        id="stamp-max-visit"
        name="max_stamps_per_visit"
        label={t('stampCards.maxPerVisit')}
        hint={t('stampCards.maxPerVisitHint')}
        inputMode="numeric"
        required
        value={draft.max_stamps_per_visit}
        onChange={(e) => set('max_stamps_per_visit', e.target.value)}
        error={msg('max_stamps_per_visit')}
      />
      <div aria-live="polite">
        {inUse && <Alert tone="warning">{t('stampCards.errors.inUse')}</Alert>}
        {!handled && <ErrorAlert error={active.error} />}
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" disabled={active.isPending || unchanged}>
          {active.isPending ? t('common.working') : t('common.save')}
        </Button>
      </div>
    </form>
  );
}
