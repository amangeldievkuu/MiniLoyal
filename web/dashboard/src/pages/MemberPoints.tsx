import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { keys, memberPointsHistoryQuery, memberPointsQuery, storesQuery } from '../api/queries';
import type { PointsAdjustment, PointsKind, PointsTransaction } from '../api/types';
import { ConfirmDialog, EmptyState, ErrorAlert } from '../components/feedback';
import { Alert, Badge, Button, Card, Dialog, Field, Status, Table, Td, Th } from '../components/ui';
import { formatDateTime } from '../lib/format';

const kindTone: Record<PointsKind, 'success' | 'neutral' | 'warning' | 'danger'> = {
  earn: 'success',
  redeem: 'neutral',
  adjust: 'warning',
  expire: 'danger',
};

/** Signed points with an explicit plus for gains. */
export function signed(n: number): string {
  return n > 0 ? `+${n.toLocaleString()}` : n.toLocaleString();
}

/** Balance, totals and upcoming expiries, plus the history and the adjust action. */
export function MemberPointsPanel({
  appId,
  memberId,
  editable,
}: {
  appId: string;
  memberId: string;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const summary = useQuery(memberPointsQuery(memberId));
  const [adjusting, setAdjusting] = useState(false);

  return (
    <>
      <Card
        title={t('points.summary.title')}
        actions={
          editable && (
            <Button variant="secondary" onClick={() => setAdjusting(true)}>
              {t('points.adjust.open')}
            </Button>
          )
        }
      >
        {summary.isPending ? (
          <Status>{t('common.loading')}</Status>
        ) : summary.isError ? (
          <ErrorAlert error={summary.error} />
        ) : (
          <dl className="grid gap-4 text-sm sm:grid-cols-3">
            <div>
              <dt className="text-zinc-500">{t('points.summary.balance')}</dt>
              <dd className="text-2xl font-semibold" data-testid="points-balance">
                {summary.data.balance.toLocaleString()}
              </dd>
            </div>
            <div>
              <dt className="text-zinc-500">{t('points.summary.lifetimeEarned')}</dt>
              <dd className="text-lg font-medium">
                {summary.data.lifetime_earned.toLocaleString()}
              </dd>
            </div>
            <div>
              <dt className="text-zinc-500">{t('points.summary.expiring')}</dt>
              <dd>
                {summary.data.expiring.length === 0 ? (
                  t('points.summary.noExpiring')
                ) : (
                  <ul className="space-y-0.5">
                    {summary.data.expiring.map((e) => (
                      <li key={e.expires_at}>
                        {t('points.summary.expiringItem', {
                          points: e.points.toLocaleString(),
                          date: formatDateTime(e.expires_at),
                        })}
                      </li>
                    ))}
                  </ul>
                )}
              </dd>
            </div>
          </dl>
        )}
      </Card>

      <PointsHistory appId={appId} memberId={memberId} />

      {editable && summary.isSuccess && (
        <AdjustDialog
          open={adjusting}
          memberId={memberId}
          balance={summary.data.balance}
          onClose={() => setAdjusting(false)}
        />
      )}
    </>
  );
}

function PointsHistory({ appId, memberId }: { appId: string; memberId: string }) {
  const { t } = useTranslation();
  const history = useInfiniteQuery(memberPointsHistoryQuery(memberId));
  const stores = useQuery(storesQuery(appId));
  const storeName = (id: string | null | undefined) =>
    id ? (stores.data?.find((s) => s.id === id)?.name ?? id.slice(0, 8)) : '';

  const items: PointsTransaction[] = history.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <Card title={t('points.history.title')}>
      {history.isPending ? (
        <Status>{t('common.loading')}</Status>
      ) : history.isError ? (
        <ErrorAlert error={history.error} />
      ) : items.length === 0 ? (
        <EmptyState>{t('points.history.empty')}</EmptyState>
      ) : (
        <>
          <Table>
            <thead>
              <tr>
                <Th>{t('points.history.kind')}</Th>
                <Th className="text-right">{t('points.history.points')}</Th>
                <Th className="text-right">{t('points.history.balanceAfter')}</Th>
                <Th className="text-right">{t('points.history.spend')}</Th>
                <Th>{t('points.history.reference')}</Th>
                <Th>{t('points.history.note')}</Th>
                <Th>{t('points.history.store')}</Th>
                <Th>{t('points.history.actor')}</Th>
                <Th>{t('points.history.time')}</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {items.map((tx) => (
                <tr key={tx.id}>
                  <Td>
                    <Badge tone={kindTone[tx.kind]}>{t(`points.kinds.${tx.kind}`)}</Badge>
                  </Td>
                  <Td
                    className={
                      tx.points < 0
                        ? 'text-right text-red-700 tabular-nums dark:text-red-400'
                        : 'text-right text-emerald-700 tabular-nums dark:text-emerald-400'
                    }
                  >
                    {signed(tx.points)}
                  </Td>
                  <Td className="text-right tabular-nums">{tx.balance_after.toLocaleString()}</Td>
                  <Td className="text-right tabular-nums">
                    {tx.spend_amount != null ? `NT$${tx.spend_amount.toLocaleString()}` : ''}
                  </Td>
                  <Td className="font-mono text-xs break-all">
                    {[tx.ref_type, tx.ref_id].filter(Boolean).join(': ')}
                  </Td>
                  <Td className="break-words">{tx.note ?? ''}</Td>
                  <Td>{storeName(tx.store_id)}</Td>
                  <Td>{t(`points.actors.${tx.actor_type}`)}</Td>
                  <Td className="whitespace-nowrap">{formatDateTime(tx.created_at)}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
          {history.hasNextPage && (
            <div className="mt-4 flex justify-center">
              <Button
                variant="secondary"
                onClick={() => void history.fetchNextPage()}
                disabled={history.isFetchingNextPage}
              >
                {history.isFetchingNextPage ? t('common.loading') : t('points.history.loadMore')}
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  );
}

/** Signed correction with a required reason; the API refuses a negative balance (409). */
export function AdjustDialog({
  open,
  memberId,
  balance,
  onClose,
}: {
  open: boolean;
  memberId: string;
  balance: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [delta, setDelta] = useState('');
  const [note, setNote] = useState('');

  const adjust = useIdempotentMutation(
    async (body: PointsAdjustment, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/members/{memberId}/points/adjustments', {
          params: { path: { memberId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: keys.memberPointsRoot(memberId) });
        close();
      },
    },
  );

  const parsed = /^-?\d+$/.test(delta.trim()) ? Number(delta.trim()) : NaN;
  const validDelta = Number.isInteger(parsed) && parsed !== 0;
  const resulting = validDelta ? balance + parsed : balance;
  const err = asApiError(adjust.error);
  const insufficient = err?.status === 409 || err?.code === 'insufficient_points';

  function close() {
    setDelta('');
    setNote('');
    adjust.reset();
    onClose();
  }

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!validDelta || !note.trim()) return;
    adjust.mutate({ delta: parsed, note: note.trim() });
  }

  return (
    <Dialog
      open={open}
      onClose={close}
      title={t('points.adjust.title')}
      description={t('points.adjust.help')}
    >
      <form onSubmit={submit} className="space-y-4" aria-busy={adjust.isPending}>
        <Field
          id="adjust-delta"
          name="delta"
          label={t('points.adjust.delta')}
          hint={t('points.adjust.deltaHint')}
          inputMode="numeric"
          required
          value={delta}
          onChange={(e) => setDelta(e.target.value)}
          error={
            delta.trim() !== '' && !validDelta
              ? t('points.adjust.invalidDelta')
              : err?.field('delta')
          }
        />
        <Field
          id="adjust-note"
          name="note"
          label={t('points.adjust.note')}
          required
          maxLength={200}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          error={err?.field('note')}
        />
        <p aria-live="polite" className="text-sm" data-testid="adjust-resulting">
          {t('points.adjust.resulting', { balance: resulting.toLocaleString() })}
        </p>
        {validDelta && resulting < 0 && (
          <Alert tone="warning">{t('points.adjust.negativeWarning')}</Alert>
        )}
        {insufficient ? (
          <Alert tone="danger">{t('points.adjust.insufficient')}</Alert>
        ) : (
          <ErrorAlert error={adjust.error} />
        )}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={close} disabled={adjust.isPending}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={adjust.isPending || !validDelta || !note.trim()}>
            {adjust.isPending ? t('common.working') : t('points.adjust.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Rotates the member's QR key so every code their phone showed stops working. */
export function MemberCardCard({ memberId }: { memberId: string }) {
  const { t } = useTranslation();
  const [confirming, setConfirming] = useState(false);
  const [version, setVersion] = useState<number | null>(null);

  const rotate = useIdempotentMutation(
    async (_: undefined, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/members/{memberId}/card/rotate', {
          params: { path: { memberId }, header: { 'Idempotency-Key': key } },
        }),
      ),
    {
      onSuccess: (res) => {
        setVersion(res.qr_key_version);
        setConfirming(false);
      },
    },
  );

  return (
    <Card title={t('members.card.title')} description={t('members.card.help')}>
      <Button variant="danger" onClick={() => setConfirming(true)}>
        {t('members.card.reset')}
      </Button>
      {version !== null && (
        <Alert tone="success" className="mt-3">
          {t('members.card.done', { version })}
        </Alert>
      )}
      <ConfirmDialog
        open={confirming}
        title={t('members.card.confirmTitle')}
        body={t('members.card.confirmBody')}
        confirmLabel={t('members.card.reset')}
        pending={rotate.isPending}
        error={rotate.error}
        onCancel={() => {
          rotate.reset();
          setConfirming(false);
        }}
        onConfirm={() => rotate.mutate(undefined)}
      />
    </Card>
  );
}
