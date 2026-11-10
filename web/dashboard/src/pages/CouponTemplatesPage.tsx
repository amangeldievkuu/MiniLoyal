import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { useActionMutation } from '../api/actionMutation';
import { api, asApiError, unwrap } from '../api/client';
import { couponTemplatesQuery } from '../api/loyaltyQueries';
import { keys } from '../api/queries';
import {
  ARCHIVE_STATUSES,
  COUPON_TYPES,
  type ArchiveStatus,
  type CouponTemplate,
  type CouponTemplateUpdate,
  type CouponType,
  type Role,
} from '../api/types';
import {
  emptyDraft,
  toCreateBody,
  toDraft,
  toUpdateBody,
  validateCoupon,
  type CouponDraft,
} from '../features/coupons/couponForm';
import { AppHeader } from '../components/AppHeader';
import { EmptyState, ErrorAlert, PageHeader, ReadOnlyNotice } from '../components/feedback';
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
import { formatDateTime } from '../lib/format';
import { canManage } from '../lib/roles';

export function CouponTemplatesPage() {
  const { appId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>
      {(_app, role) => <CouponTemplatesView appId={appId} role={role} />}
    </AppHeader>
  );
}

/** "NT$50 off", "20% off" or "Free item", as members read it. */
export function useCouponValueText() {
  const { t } = useTranslation();
  return (type: CouponType, value: number | null | undefined) =>
    type === 'free_item'
      ? t('coupons.valueText.free_item')
      : t(`coupons.valueText.${type}`, { value: (value ?? 0).toLocaleString() });
}

type Editing = { kind: 'create' } | { kind: 'edit'; template: CouponTemplate } | null;

export function CouponTemplatesView({ appId, role }: { appId: string; role: Role | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const valueText = useCouponValueText();
  const editable = canManage(role);
  const [filter, setFilter] = useState<ArchiveStatus | ''>('');
  const [editing, setEditing] = useState<Editing>(null);
  const list = useQuery(couponTemplatesQuery(appId, filter || undefined));

  const setStatus = useActionMutation(
    async ({ id, status }: { id: string; status: ArchiveStatus }, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/coupon-templates/{templateId}', {
          params: { path: { templateId: id }, header: { 'Idempotency-Key': key } },
          body: { status },
        }),
      ),
    { onSuccess: () => void qc.invalidateQueries({ queryKey: keys.couponTemplatesRoot(appId) }) },
  );

  return (
    <div>
      <PageHeader
        title={t('coupons.title')}
        description={t('coupons.help')}
        actions={
          editable && (
            <Button onClick={() => setEditing({ kind: 'create' })}>{t('coupons.create')}</Button>
          )
        }
      />
      {!editable && <ReadOnlyNotice />}
      <div className="mb-4 max-w-xs">
        <SelectField
          id="coupon-filter"
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
          <EmptyState>{t('coupons.empty')}</EmptyState>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t('coupons.name')}</Th>
                <Th>{t('coupons.type')}</Th>
                <Th className="text-right">{t('coupons.claimedCount')}</Th>
                <Th>{t('coupons.claimable')}</Th>
                <Th>{t('coupons.validTo')}</Th>
                <Th>{t('common.status')}</Th>
                {editable && <Th className="text-right">{t('common.actions')}</Th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {list.data.map((tpl) => (
                <tr key={tpl.id}>
                  <Td className="font-medium">{tpl.name}</Td>
                  <Td>{valueText(tpl.type, tpl.value)}</Td>
                  <Td className="text-right tabular-nums">
                    {tpl.total_limit == null
                      ? tpl.claimed_count.toLocaleString()
                      : `${tpl.claimed_count.toLocaleString()} / ${tpl.total_limit.toLocaleString()}`}
                  </Td>
                  <Td>{tpl.claimable ? t('coupons.yes') : t('coupons.no')}</Td>
                  <Td className="whitespace-nowrap">
                    {tpl.valid_to ? formatDateTime(tpl.valid_to) : t('coupons.noEnd')}
                  </Td>
                  <Td>
                    <Badge tone={tpl.status === 'active' ? 'success' : 'warning'}>
                      {t(`statuses.${tpl.status}`)}
                    </Badge>
                  </Td>
                  {editable && (
                    <Td className="text-right whitespace-nowrap">
                      <Button
                        variant="ghost"
                        aria-label={`${t('common.edit')}: ${tpl.name}`}
                        onClick={() => setEditing({ kind: 'edit', template: tpl })}
                      >
                        {t('common.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        aria-label={`${tpl.status === 'active' ? t('coupons.archive') : t('coupons.reactivate')}: ${tpl.name}`}
                        disabled={setStatus.isPending}
                        onClick={() =>
                          setStatus.mutate({
                            id: tpl.id,
                            status: tpl.status === 'active' ? 'archived' : 'active',
                          })
                        }
                      >
                        {tpl.status === 'active' ? t('coupons.archive') : t('coupons.reactivate')}
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
        title={editing?.kind === 'edit' ? t('coupons.edit') : t('coupons.create')}
      >
        {editing && (
          <CouponForm
            key={editing.kind === 'edit' ? editing.template.id : 'create'}
            appId={appId}
            template={editing.kind === 'edit' ? editing.template : null}
            onClose={() => setEditing(null)}
          />
        )}
      </Dialog>
    </div>
  );
}

function CouponForm({
  appId,
  template,
  onClose,
}: {
  appId: string;
  template: CouponTemplate | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [draft, setDraft] = useState<CouponDraft>(() =>
    template ? toDraft(template) : emptyDraft,
  );
  const [staleInUse, setStaleInUse] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const locked = (template?.claimed_count ?? 0) > 0 || staleInUse;
  const errors = validateCoupon(draft);

  const done = () => {
    void qc.invalidateQueries({ queryKey: keys.couponTemplatesRoot(appId) });
    onClose();
  };

  const create = useActionMutation(
    async (body: ReturnType<typeof toCreateBody>, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/apps/{appId}/coupon-templates', {
          params: { path: { appId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    { onSuccess: done },
  );
  const update = useActionMutation(
    async (body: CouponTemplateUpdate, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/coupon-templates/{templateId}', {
          params: { path: { templateId: template?.id ?? '' }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: done,
      onError: (e) => {
        if (asApiError(e)?.code === 'coupon_template_in_use') setStaleInUse(true);
      },
    },
  );

  const active = template ? update : create;
  const err = asApiError(active.error);
  const inUse = err?.code === 'coupon_template_in_use';
  const serverField = (name: string) => err?.field(name);
  const handled = inUse || serverField('total_limit') !== undefined;

  const updateBody = template ? toUpdateBody(draft, template, locked) : null;
  const unchanged = updateBody !== null && Object.keys(updateBody).length === 0;
  const hasErrors = Object.keys(errors).length > 0;

  function set<K extends keyof CouponDraft>(field: K, value: CouponDraft[K]) {
    setDraft((d) => ({ ...d, [field]: value }));
  }
  const msg = (k: keyof CouponDraft) => {
    const key = errors[k];
    return (submitted && key ? t(`coupons.errors.${key}`) : undefined) ?? serverField(k);
  };

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setSubmitted(true);
    if (hasErrors) return;
    if (template && updateBody) update.mutate(updateBody);
    else create.mutate(toCreateBody(draft));
  }

  return (
    <form onSubmit={submit} noValidate className="space-y-4" aria-busy={active.isPending}>
      <Field
        id="coupon-name"
        name="name"
        label={t('coupons.name')}
        required
        maxLength={100}
        value={draft.name}
        onChange={(e) => set('name', e.target.value)}
        error={msg('name')}
      />
      <Field
        id="coupon-description"
        name="description"
        label={`${t('coupons.description')} (${t('common.optional')})`}
        maxLength={500}
        value={draft.description}
        onChange={(e) => set('description', e.target.value)}
        error={msg('description')}
      />
      <SelectField
        id="coupon-type"
        name="type"
        label={t('coupons.type')}
        value={draft.type}
        disabled={locked}
        hint={locked ? t('coupons.lockedHint') : undefined}
        onChange={(e) => set('type', e.target.value as CouponType)}
        error={inUse ? t('coupons.errors.inUse') : undefined}
      >
        {COUPON_TYPES.map((ty) => (
          <option key={ty} value={ty}>
            {t(`coupons.types.${ty}`)}
          </option>
        ))}
      </SelectField>
      {draft.type !== 'free_item' && (
        <Field
          id="coupon-value"
          name="value"
          label={draft.type === 'amount_off' ? t('coupons.valueAmount') : t('coupons.valuePercent')}
          inputMode="numeric"
          required
          disabled={locked}
          value={draft.value}
          onChange={(e) => set('value', e.target.value)}
          error={msg('value')}
        />
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          id="coupon-total"
          name="total_limit"
          label={t('coupons.totalLimit')}
          hint={t('coupons.totalLimitHint', { n: template?.claimed_count ?? 0 })}
          inputMode="numeric"
          value={draft.total_limit}
          onChange={(e) => set('total_limit', e.target.value)}
          error={msg('total_limit')}
        />
        <Field
          id="coupon-per-member"
          name="per_member_limit"
          label={t('coupons.perMemberLimit')}
          inputMode="numeric"
          required
          value={draft.per_member_limit}
          onChange={(e) => set('per_member_limit', e.target.value)}
          error={msg('per_member_limit')}
        />
        <Field
          id="coupon-valid-from"
          name="valid_from"
          type="datetime-local"
          label={t('coupons.validFrom')}
          value={draft.valid_from}
          onChange={(e) => set('valid_from', e.target.value)}
          error={msg('valid_from')}
        />
        <Field
          id="coupon-valid-to"
          name="valid_to"
          type="datetime-local"
          label={t('coupons.validTo')}
          value={draft.valid_to}
          onChange={(e) => set('valid_to', e.target.value)}
          error={msg('valid_to')}
        />
        <Field
          id="coupon-validity-days"
          name="validity_days"
          label={t('coupons.validityDays')}
          hint={t('coupons.validityDaysHint')}
          inputMode="numeric"
          value={draft.validity_days}
          onChange={(e) => set('validity_days', e.target.value)}
          error={msg('validity_days')}
        />
      </div>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          name="claimable"
          className="mt-1"
          checked={draft.claimable}
          onChange={(e) => set('claimable', e.target.checked)}
        />
        <span>
          {t('coupons.claimable')}
          <span className="block text-xs text-zinc-500">{t('coupons.claimableHint')}</span>
        </span>
      </label>
      <div aria-live="polite">{!handled && <ErrorAlert error={active.error} />}</div>
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
