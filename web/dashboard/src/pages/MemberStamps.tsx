import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { useActionMutation } from '../api/actionMutation';
import { api, asApiError, unwrap } from '../api/client';
import { memberStampsQuery } from '../api/loyaltyQueries';
import { keys } from '../api/queries';
import type { StampProgress, StampResult } from '../api/types';
import { EmptyState, ErrorAlert } from '../components/feedback';
import { Alert, Badge, Button, Card, Dialog, Field, Status } from '../components/ui';

/** Stamp progress per card; owners and admins can correct it with a note. */
export function MemberStampsPanel({ memberId, editable }: { memberId: string; editable: boolean }) {
  const { t } = useTranslation();
  const stamps = useQuery(memberStampsQuery(memberId));
  const [adjusting, setAdjusting] = useState<StampProgress | null>(null);
  const [result, setResult] = useState<StampResult | null>(null);

  return (
    <Card title={t('memberStamps.title')}>
      {stamps.isPending ? (
        <Status>{t('common.loading')}</Status>
      ) : stamps.isError ? (
        <ErrorAlert error={stamps.error} />
      ) : stamps.data.length === 0 ? (
        <EmptyState>{t('memberStamps.empty')}</EmptyState>
      ) : (
        <ul className="space-y-4">
          {stamps.data.map((p) => (
            <li
              key={p.stamp_card_id}
              className="rounded-md border border-zinc-200 p-4 dark:border-zinc-800"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <h3 className="font-medium">{p.name}</h3>
                  {p.status === 'archived' && (
                    <Badge tone="warning">{t('statuses.archived')}</Badge>
                  )}
                </div>
                {editable && (
                  <Button
                    variant="secondary"
                    aria-label={`${t('memberStamps.adjust')}: ${p.name}`}
                    onClick={() => setAdjusting(p)}
                  >
                    {t('memberStamps.adjust')}
                  </Button>
                )}
              </div>
              <dl className="mt-3 grid gap-3 text-sm sm:grid-cols-3">
                <div>
                  <dt className="text-zinc-500">{t('memberStamps.progress')}</dt>
                  <dd className="tabular-nums" data-testid={`stamp-progress-${p.stamp_card_id}`}>
                    {t('memberStamps.progressValue', {
                      count: p.count,
                      required: p.stamps_required,
                    })}
                  </dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('memberStamps.completions')}</dt>
                  <dd className="tabular-nums">{p.completions}</dd>
                </div>
                <div>
                  <dt className="text-zinc-500">{t('memberStamps.reward')}</dt>
                  <dd>{p.reward?.name ?? t('stampCards.noReward')}</dd>
                </div>
              </dl>
              {p.unrewarded_completions > 0 && (
                <Alert tone="warning" className="mt-3">
                  <p className="font-medium">
                    {t('memberStamps.unrewarded', { n: p.unrewarded_completions })}
                  </p>
                  <p className="text-sm">{t('memberStamps.unrewardedHint')}</p>
                </Alert>
              )}
            </li>
          ))}
        </ul>
      )}
      {result && (
        <div aria-live="polite" className="mt-4">
          <Alert tone={result.rewards_unavailable > 0 ? 'warning' : 'success'}>
            {result.rewards_unavailable > 0
              ? t('memberStamps.doneUnavailable', { n: result.rewards_unavailable })
              : t('memberStamps.done', { n: result.completed })}
          </Alert>
        </div>
      )}
      {editable && (
        <AdjustStampsDialog
          progress={adjusting}
          memberId={memberId}
          onDone={(r) => setResult(r)}
          onClose={() => setAdjusting(null)}
        />
      )}
    </Card>
  );
}

export function AdjustStampsDialog({
  progress,
  memberId,
  onDone,
  onClose,
}: {
  progress: StampProgress | null;
  memberId: string;
  onDone: (r: StampResult) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [delta, setDelta] = useState('');
  const [note, setNote] = useState('');

  const adjust = useActionMutation(
    async (body: { stamp_card_id: string; delta: number; note: string }, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/members/{memberId}/stamps/adjustments', {
          params: { path: { memberId }, header: { 'Idempotency-Key': key } },
          body,
        }),
      ),
    {
      onSuccess: (r) => {
        void qc.invalidateQueries({ queryKey: keys.memberStamps(memberId) });
        void qc.invalidateQueries({ queryKey: keys.memberCouponsRoot(memberId) });
        onDone(r);
        close();
      },
    },
  );

  const parsed = /^-?\d+$/.test(delta.trim()) ? Number(delta.trim()) : NaN;
  const validDelta = Number.isInteger(parsed) && parsed !== 0 && Math.abs(parsed) <= 100;
  const err = asApiError(adjust.error);

  function close() {
    setDelta('');
    setNote('');
    adjust.reset();
    onClose();
  }
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!progress || !validDelta || !note.trim()) return;
    adjust.mutate({ stamp_card_id: progress.stamp_card_id, delta: parsed, note: note.trim() });
  }

  return (
    <Dialog
      open={progress !== null}
      onClose={close}
      title={t('memberStamps.adjustTitle', { name: progress?.name ?? '' })}
      description={t('memberStamps.adjustHelp')}
    >
      <form onSubmit={submit} className="space-y-4" aria-busy={adjust.isPending}>
        <Field
          id="stamps-delta"
          name="delta"
          label={t('memberStamps.delta')}
          hint={t('memberStamps.deltaHint')}
          inputMode="numeric"
          required
          value={delta}
          onChange={(e) => setDelta(e.target.value)}
          error={
            delta.trim() !== '' && !validDelta
              ? t('memberStamps.invalidDelta')
              : err?.field('delta')
          }
        />
        <Field
          id="stamps-note"
          name="note"
          label={t('memberStamps.note')}
          hint={t('memberStamps.noteHint')}
          required
          maxLength={200}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          error={err?.field('note')}
        />
        <div aria-live="polite">
          {err?.code === 'insufficient_stamps' ? (
            <Alert tone="danger">{t('memberStamps.insufficient')}</Alert>
          ) : (
            <ErrorAlert error={adjust.error} />
          )}
        </div>
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
