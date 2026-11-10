import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { useActionMutation } from '../api/actionMutation';
import { api, asApiError, unwrap } from '../api/client';
import { couponTemplatesQuery, memberCouponsQuery } from '../api/loyaltyQueries';
import { keys } from '../api/queries';
import {
  COUPON_STATES,
  type CouponInstance,
  type CouponState,
  type CouponTemplate,
} from '../api/types';
import { EmptyState, ErrorAlert } from '../components/feedback';
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
import { formatDateTime } from '../lib/format';
import { useCouponValueText } from './CouponTemplatesPage';

const stateTone: Record<CouponState, 'success' | 'neutral' | 'warning' | 'danger'> = {
  available: 'success',
  redeemed: 'neutral',
  expired: 'warning',
  voided: 'danger',
};

/** The member's coupons with a state filter; owners and admins can issue and void. */
export function MemberCouponsPanel({
  appId,
  memberId,
  editable,
}: {
  appId: string;
  memberId: string;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const valueText = useCouponValueText();
  const [state, setState] = useState<CouponState | ''>('');
  const [issuing, setIssuing] = useState(false);
  const [voiding, setVoiding] = useState<CouponInstance | null>(null);
  const coupons = useInfiniteQuery(memberCouponsQuery(memberId, state || undefined));
  const items = coupons.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <Card
      title={t('memberCoupons.title')}
      actions={
        editable && (
          <Button variant="secondary" onClick={() => setIssuing(true)}>
            {t('memberCoupons.issue')}
          </Button>
        )
      }
    >
      <div className="mb-4 max-w-xs">
        <SelectField
          id="member-coupon-state"
          label={t('memberCoupons.state')}
          value={state}
          onChange={(e) => setState(e.target.value as CouponState | '')}
        >
          <option value="">{t('common.all')}</option>
          {COUPON_STATES.map((s) => (
            <option key={s} value={s}>
              {t(`memberCoupons.states.${s}`)}
            </option>
          ))}
        </SelectField>
      </div>
      {coupons.isPending ? (
        <Status>{t('common.loading')}</Status>
      ) : coupons.isError ? (
        <ErrorAlert error={coupons.error} />
      ) : items.length === 0 ? (
        <EmptyState>{t('memberCoupons.empty')}</EmptyState>
      ) : (
        <>
          <Table>
            <thead>
              <tr>
                <Th>{t('memberCoupons.coupon')}</Th>
                <Th>{t('memberCoupons.code')}</Th>
                <Th>{t('common.status')}</Th>
                <Th>{t('memberCoupons.source')}</Th>
                <Th>{t('memberCoupons.issuedAt')}</Th>
                <Th>{t('memberCoupons.expiresAt')}</Th>
                {editable && <Th className="text-right">{t('common.actions')}</Th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {items.map((c) => (
                <tr key={c.id}>
                  <Td>
                    <span className="font-medium">{c.template.name}</span>
                    <span className="block text-xs text-zinc-500">
                      {valueText(c.template.type, c.template.value)}
                    </span>
                  </Td>
                  <Td className="font-mono text-xs">{c.code}</Td>
                  <Td>
                    <Badge tone={stateTone[c.state]}>{t(`memberCoupons.states.${c.state}`)}</Badge>
                  </Td>
                  <Td>{t(`memberCoupons.sources.${c.source}`)}</Td>
                  <Td className="whitespace-nowrap">{formatDateTime(c.issued_at)}</Td>
                  <Td className="whitespace-nowrap">
                    {c.expires_at ? formatDateTime(c.expires_at) : t('memberCoupons.never')}
                  </Td>
                  {editable && (
                    <Td className="text-right">
                      {c.status === 'issued' && (
                        <Button
                          variant="ghost"
                          aria-label={`${t('memberCoupons.void')}: ${c.code}`}
                          onClick={() => setVoiding(c)}
                        >
                          {t('memberCoupons.void')}
                        </Button>
                      )}
                    </Td>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
          {coupons.hasNextPage && (
            <div className="mt-4 flex justify-center">
              <Button
                variant="secondary"
                onClick={() => void coupons.fetchNextPage()}
                disabled={coupons.isFetchingNextPage}
              >
                {coupons.isFetchingNextPage ? t('common.loading') : t('points.history.loadMore')}
              </Button>
            </div>
          )}
        </>
      )}
      {editable && (
        <>
          <IssueDialog
            open={issuing}
            appId={appId}
            memberId={memberId}
            onClose={() => setIssuing(false)}
          />
          <VoidDialog coupon={voiding} memberId={memberId} onClose={() => setVoiding(null)} />
        </>
      )}
    </Card>
  );
}

const issueErrors = {
  coupon_sold_out: 'memberCoupons.errors.soldOut',
  coupon_not_claimable: 'memberCoupons.errors.notClaimable',
} as const;

export function IssueDialog({
  open,
  appId,
  memberId,
  onClose,
}: {
  open: boolean;
  appId: string;
  memberId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const valueText = useCouponValueText();
  const templates = useQuery({ ...couponTemplatesQuery(appId, 'active'), enabled: open });
  const [templateId, setTemplateId] = useState('');

  const issue = useActionMutation(
    async (body: { template_id: string }, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/members/{memberId}/coupons', {
          params: { path: { memberId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: keys.memberCouponsRoot(memberId) });
        void qc.invalidateQueries({ queryKey: keys.couponTemplatesRoot(appId) });
        close();
      },
    },
  );
  const err = asApiError(issue.error);
  const mapped =
    err?.code === 'coupon_sold_out' || err?.code === 'coupon_not_claimable'
      ? issueErrors[err.code]
      : undefined;

  function close() {
    setTemplateId('');
    issue.reset();
    onClose();
  }
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (templateId) issue.mutate({ template_id: templateId });
  }

  const list: CouponTemplate[] = templates.data ?? [];
  return (
    <Dialog
      open={open}
      onClose={close}
      title={t('memberCoupons.issueTitle')}
      description={t('memberCoupons.issueHelp')}
    >
      <form onSubmit={submit} className="space-y-4" aria-busy={issue.isPending}>
        {templates.isError ? (
          <ErrorAlert error={templates.error} />
        ) : (
          <SelectField
            id="issue-template"
            name="template_id"
            label={t('memberCoupons.template')}
            required
            value={templateId}
            onChange={(e) => setTemplateId(e.target.value)}
          >
            <option value="">{t('memberCoupons.pickTemplate')}</option>
            {list.map((x) => (
              <option key={x.id} value={x.id}>
                {`${x.name} (${valueText(x.type, x.value)})`}
              </option>
            ))}
          </SelectField>
        )}
        <div aria-live="polite">
          {mapped ? <Alert tone="danger">{t(mapped)}</Alert> : <ErrorAlert error={issue.error} />}
        </div>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={close} disabled={issue.isPending}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={issue.isPending || !templateId}>
            {issue.isPending ? t('common.working') : t('memberCoupons.issueSubmit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function VoidDialog({
  coupon,
  memberId,
  onClose,
}: {
  coupon: CouponInstance | null;
  memberId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [reason, setReason] = useState('');

  const voidIt = useActionMutation(
    async ({ id, reason: r }: { id: string; reason: string }, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/coupons/{instanceId}/void', {
          params: { path: { instanceId: id }, header: { 'Idempotency-Key': key } },
          body: { reason: r },
        }),
      ),
    {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: keys.memberCouponsRoot(memberId) });
        close();
      },
    },
  );
  const err = asApiError(voidIt.error);
  const unavailable = err?.code === 'coupon_unavailable';

  function close() {
    setReason('');
    voidIt.reset();
    onClose();
  }
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (coupon && reason.trim()) voidIt.mutate({ id: coupon.id, reason: reason.trim() });
  }

  return (
    <Dialog
      open={coupon !== null}
      onClose={close}
      title={t('memberCoupons.voidTitle')}
      description={t('memberCoupons.voidHelp', { code: coupon?.code ?? '' })}
    >
      <form onSubmit={submit} className="space-y-4" aria-busy={voidIt.isPending}>
        <Field
          id="void-reason"
          name="reason"
          label={t('memberCoupons.reason')}
          hint={t('memberCoupons.reasonHint')}
          required
          maxLength={200}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          error={err?.field('reason')}
        />
        <div aria-live="polite">
          {unavailable ? (
            <Alert tone="danger">{t('memberCoupons.errors.unavailable')}</Alert>
          ) : (
            <ErrorAlert error={voidIt.error} />
          )}
        </div>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={close} disabled={voidIt.isPending}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" variant="danger" disabled={voidIt.isPending || !reason.trim()}>
            {voidIt.isPending ? t('common.working') : t('memberCoupons.void')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
