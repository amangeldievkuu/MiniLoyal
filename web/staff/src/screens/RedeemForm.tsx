import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import type { ApiError } from '../api/client';
import type { PointsTransaction, ScanResult } from '../api/types';
import { Keypad } from '../components/Keypad';
import { MAX_POINTS } from '../lib/limits';
import { Alert, Button, Card } from '../components/ui';
import type { Outcome, RedeemBody } from '../lib/pending';
import { describeActionError } from './actionError';

/** Spends points: the amount is capped at the balance and confirmed before sending. */
export function RedeemForm({
  scan,
  blocked,
  onSubmit,
  onDone,
  onScanAgain,
}: {
  scan: ScanResult;
  /** A write for this member is still pending: no new one until it is settled. */
  blocked: boolean;
  onSubmit: (body: RedeemBody) => Promise<Outcome>;
  onDone: (txn: PointsTransaction) => void;
  onScanAgain: () => void;
}) {
  const { t } = useTranslation();
  const [points, setPoints] = useState('');
  const [reviewing, setReviewing] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const n = Number(points || '0');
  const over = n > scan.balance;
  const failure = error ? describeActionError(error, t, scan.balance) : null;

  function change(next: string) {
    setPoints(next);
    setReviewing(false);
    setError(null);
  }

  function confirm() {
    if (pending || blocked) return;
    setPending(true);
    setError(null);
    void onSubmit({ scan_token: scan.scan_token, points: n })
      .then((out) => {
        if (out.ok) onDone(out.txn);
        else if (!out.ambiguous) setError(out.error);
      })
      .finally(() => setPending(false));
  }

  return (
    <div className="flex flex-col gap-4" aria-busy={pending}>
      <Keypad
        id="redeem-points"
        label={t('redeem.pointsLabel')}
        value={points}
        onChange={change}
        max={MAX_POINTS}
      />
      {over && (
        <Alert tone="warning">
          {t('redeem.overBalance', { balance: scan.balance.toLocaleString() })}
        </Alert>
      )}
      {blocked && <Alert tone="warning">{t('pending.blocked')}</Alert>}
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
      {reviewing ? (
        <Card className="flex flex-col gap-3">
          <h3 className="text-lg font-semibold">{t('redeem.confirmTitle')}</h3>
          <p className="text-base">
            {t('redeem.confirmBody', {
              points: n.toLocaleString(),
              balance: (scan.balance - n).toLocaleString(),
            })}
          </p>
          <div className="grid grid-cols-2 gap-2">
            <Button variant="secondary" onClick={() => setReviewing(false)} disabled={pending}>
              {t('common.back')}
            </Button>
            <Button onClick={confirm} disabled={pending || blocked || failure?.scanAgain}>
              {pending ? t('common.working') : t('redeem.confirm')}
            </Button>
          </div>
        </Card>
      ) : (
        <Button size="lg" disabled={n <= 0 || over || blocked} onClick={() => setReviewing(true)}>
          {t('redeem.review')}
        </Button>
      )}
    </div>
  );
}
