import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import type { ApiError } from '../api/client';
import type { ScanResult, StampProgress, StampResult } from '../api/types';
import { Alert, Button, Card } from '../components/ui';
import type { WriteOutcome } from '../lib/pending';
import { describeActionError } from './actionError';
import { couponValue } from './format';

/** Stamps still allowed on this scan for a card (the cap counts those already given). */
export function remainingStamps(card: StampProgress, given: number): number {
  return Math.max(0, card.max_stamps_per_visit - given);
}

/** Active stamp cards with progress and a count stepper capped per visit. */
export function StampsPanel({
  scan,
  given,
  results,
  blocked,
  onAdd,
  onScanAgain,
}: {
  scan: ScanResult;
  /** Stamps already added per card on this scan. */
  given: Record<string, number>;
  /** The latest API result per card on this scan. */
  results: Record<string, StampResult>;
  blocked: boolean;
  onAdd: (card: StampProgress, count: number) => Promise<WriteOutcome>;
  onScanAgain: () => void;
}) {
  const { t } = useTranslation();
  if (scan.stamps.length === 0) return <p className="py-2 text-base">{t('stamps.none')}</p>;
  return (
    <div className="flex flex-col gap-3">
      {blocked && <Alert tone="warning">{t('pending.blocked')}</Alert>}
      <ul className="flex flex-col gap-3">
        {scan.stamps.map((card) => (
          <li key={card.stamp_card_id}>
            <StampCard
              card={card}
              given={given[card.stamp_card_id] ?? 0}
              result={results[card.stamp_card_id]}
              blocked={blocked}
              onAdd={onAdd}
              onScanAgain={onScanAgain}
            />
          </li>
        ))}
      </ul>
    </div>
  );
}

function StampCard({
  card,
  given,
  result,
  blocked,
  onAdd,
  onScanAgain,
}: {
  card: StampProgress;
  given: number;
  result: StampResult | undefined;
  blocked: boolean;
  onAdd: (card: StampProgress, count: number) => Promise<WriteOutcome>;
  onScanAgain: () => void;
}) {
  const { t } = useTranslation();
  const remaining = remainingStamps(card, given);
  const [wanted, setWanted] = useState(1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const count = Math.min(Math.max(wanted, 1), Math.max(remaining, 1));
  const failure = error ? describeActionError(error, t, 0) : null;

  function add() {
    if (busy || blocked || remaining === 0) return;
    setBusy(true);
    setError(null);
    void onAdd(card, count)
      .then((out) => {
        if (out.ok) setWanted(1);
        else if (!out.ambiguous) setError(out.error);
      })
      .finally(() => setBusy(false));
  }

  return (
    <Card className="flex flex-col gap-3">
      <div>
        <p className="text-lg font-semibold">{card.name}</p>
        <p className="text-base">
          {t('stamps.progress', { count: card.count, required: card.stamps_required })}
        </p>
        {card.reward && (
          <p className="text-sm">
            {t('stamps.reward', { name: card.reward.name, value: couponValue(card.reward, t) })}
          </p>
        )}
      </div>
      {result && <StampOutcome result={result} />}
      {failure && (
        <Alert tone="danger">
          <p>{failure.message}</p>
          {failure.scanAgain && (
            <Button variant="secondary" className="mt-2" onClick={onScanAgain}>
              {t('scan.button')}
            </Button>
          )}
        </Alert>
      )}
      {remaining === 0 ? (
        <p className="text-sm">{t('stamps.limitReached', { max: card.max_stamps_per_visit })}</p>
      ) : (
        <>
          <div
            role="group"
            aria-label={t('stamps.countLabel')}
            className="grid grid-cols-[3rem_1fr_3rem] items-center gap-2"
          >
            <Button
              variant="secondary"
              aria-label={t('stamps.less')}
              className="min-w-12"
              disabled={busy || blocked || count <= 1}
              onClick={() => setWanted(count - 1)}
            >
              -
            </Button>
            <output className="text-center text-2xl font-bold" aria-live="polite">
              {count}
            </output>
            <Button
              variant="secondary"
              aria-label={t('stamps.more')}
              className="min-w-12"
              disabled={busy || blocked || count >= remaining}
              onClick={() => setWanted(count + 1)}
            >
              +
            </Button>
          </div>
          <p className="text-sm">
            {t('stamps.cap', { remaining, max: card.max_stamps_per_visit })}
          </p>
          <Button
            onClick={add}
            disabled={busy || blocked || failure?.scanAgain}
            aria-label={t('stamps.addNamed', { name: card.name, count })}
          >
            {busy ? t('common.working') : t('stamps.add', { count })}
          </Button>
        </>
      )}
    </Card>
  );
}

/** What the API said about the last stamp request on a card. */
function StampOutcome({ result }: { result: StampResult }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2">
      <Alert tone="success">
        <p className="font-semibold">{t('stamps.added', { count: result.added })}</p>
        {result.completed > 0 && (
          <p className="font-semibold">{t('stamps.completed', { count: result.completed })}</p>
        )}
        {result.rewards.map((r) => (
          <p key={r.id}>{t('stamps.rewardIssued', { name: r.template.name })}</p>
        ))}
      </Alert>
      {result.rewards_unavailable > 0 && (
        <Alert tone="warning">
          {t('stamps.rewardUnavailable', { count: result.rewards_unavailable })}
        </Alert>
      )}
    </div>
  );
}
