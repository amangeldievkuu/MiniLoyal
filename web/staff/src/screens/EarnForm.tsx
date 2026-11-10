import { useCallback, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, unwrap } from '../api/client';
import type { PointsTransaction, ScanResult } from '../api/types';
import { Keypad } from '../components/Keypad';
import { Alert, Button } from '../components/ui';
import { useIdempotentSubmit } from '../lib/idempotency';
import { describeActionError } from './actionError';

/**
 * Points for a purchase. The server applies the app's rule to the amount;
 * managers may give a fixed number of points instead.
 */
export function EarnForm({
  scan,
  isManager,
  onDone,
  onScanAgain,
}: {
  scan: ScanResult;
  isManager: boolean;
  onDone: (txn: PointsTransaction) => void;
  onScanAgain: () => void;
}) {
  const { t } = useTranslation();
  const [amount, setAmount] = useState('');
  const [fixed, setFixed] = useState(false);

  const run = useCallback(
    async (key: string) =>
      unwrap(
        await api.POST('/v1/staff/points/earn', {
          params: { header: { 'Idempotency-Key': key } },
          body: fixed
            ? { scan_token: scan.scan_token, points: Number(amount) }
            : { scan_token: scan.scan_token, spend_amount: Number(amount) },
        }),
      ),
    [amount, fixed, scan.scan_token],
  );
  const { submit, pending, error, invalidateKey } = useIdempotentSubmit(run);
  const failure = error ? describeActionError(error, t, scan.balance) : null;

  function change(next: string) {
    setAmount(next);
    invalidateKey();
  }

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!amount || Number(amount) <= 0) return;
    void submit().then((txn) => {
      if (txn) onDone(txn);
    });
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4" aria-busy={pending}>
      {isManager && (
        <label className="flex min-h-12 items-center gap-3 text-base">
          <input
            type="checkbox"
            checked={fixed}
            onChange={(e) => {
              setFixed(e.target.checked);
              change('');
            }}
            className="size-6"
          />
          {t('earn.fixedPoints')}
        </label>
      )}
      <Keypad
        id="earn-amount"
        label={fixed ? t('earn.pointsLabel') : t('earn.amountLabel')}
        prefix={fixed ? undefined : 'NT$'}
        value={amount}
        onChange={change}
      />
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
      <Button
        type="submit"
        size="lg"
        disabled={pending || !amount || Number(amount) <= 0 || failure?.scanAgain}
      >
        {pending ? t('common.working') : failure?.retryable ? t('common.retry') : t('earn.submit')}
      </Button>
    </form>
  );
}
