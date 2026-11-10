import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api, unwrap } from '../api/client';
import type { PointsTransaction, ScanResult } from '../api/types';
import { Keypad } from '../components/Keypad';
import { Alert, Button, Card } from '../components/ui';
import { useIdempotentSubmit } from '../lib/idempotency';
import { describeActionError } from './actionError';

/** Spends points: the amount is capped at the balance and confirmed before sending. */
export function RedeemForm({
  scan,
  onDone,
  onScanAgain,
}: {
  scan: ScanResult;
  onDone: (txn: PointsTransaction) => void;
  onScanAgain: () => void;
}) {
  const { t } = useTranslation();
  const [points, setPoints] = useState('');
  const [reviewing, setReviewing] = useState(false);
  const n = Number(points || '0');
  const over = n > scan.balance;

  const run = useCallback(
    async (key: string) =>
      unwrap(
        await api.POST('/v1/staff/points/redeem', {
          params: { header: { 'Idempotency-Key': key } },
          body: { scan_token: scan.scan_token, points: n },
        }),
      ),
    [n, scan.scan_token],
  );
  const { submit, pending, error, invalidateKey } = useIdempotentSubmit(run);
  const failure = error ? describeActionError(error, t, scan.balance) : null;

  function change(next: string) {
    setPoints(next);
    setReviewing(false);
    invalidateKey();
  }

  function confirm() {
    void submit().then((txn) => {
      if (txn) onDone(txn);
    });
  }

  return (
    <div className="flex flex-col gap-4" aria-busy={pending}>
      <Keypad id="redeem-points" label={t('redeem.pointsLabel')} value={points} onChange={change} />
      {over && (
        <Alert tone="warning">
          {t('redeem.overBalance', { balance: scan.balance.toLocaleString() })}
        </Alert>
      )}
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
            <Button onClick={confirm} disabled={pending || failure?.scanAgain}>
              {pending
                ? t('common.working')
                : failure?.retryable
                  ? t('common.retry')
                  : t('redeem.confirm')}
            </Button>
          </div>
        </Card>
      ) : (
        <Button size="lg" disabled={n <= 0 || over} onClick={() => setReviewing(true)}>
          {t('redeem.review')}
        </Button>
      )}
    </div>
  );
}
