import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import type { ApiError } from '../api/client';
import type { PointsTransaction, ScanResult } from '../api/types';
import { Keypad } from '../components/Keypad';
import { Alert, Button } from '../components/ui';
import type { EarnBody, Outcome } from '../lib/pending';
import { describeActionError } from './actionError';

/**
 * Points for a purchase. The server applies the app's rule to the amount;
 * managers may give a fixed number of points instead. The counter owns the
 * request (its Idempotency-Key and body live there until the outcome is
 * known), so this form only builds the body and shows definite failures.
 */
export function EarnForm({
  scan,
  isManager,
  blocked,
  onSubmit,
  onDone,
  onScanAgain,
}: {
  scan: ScanResult;
  isManager: boolean;
  /** A write for this member is still pending: no new one until it is settled. */
  blocked: boolean;
  onSubmit: (body: EarnBody) => Promise<Outcome>;
  onDone: (txn: PointsTransaction) => void;
  onScanAgain: () => void;
}) {
  const { t } = useTranslation();
  const [amount, setAmount] = useState('');
  const [fixed, setFixed] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const failure = error ? describeActionError(error, t, scan.balance) : null;

  function change(next: string) {
    setAmount(next);
    setError(null);
  }

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (pending || blocked || !amount || Number(amount) <= 0) return;
    const body: EarnBody = fixed
      ? { scan_token: scan.scan_token, points: Number(amount) }
      : { scan_token: scan.scan_token, spend_amount: Number(amount) };
    setPending(true);
    setError(null);
    void onSubmit(body)
      .then((out) => {
        if (out.ok) onDone(out.txn);
        else if (!out.ambiguous) setError(out.error);
      })
      .finally(() => setPending(false));
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" aria-busy={pending}>
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
      <Button
        type="submit"
        size="lg"
        disabled={pending || blocked || !amount || Number(amount) <= 0 || failure?.scanAgain}
      >
        {pending ? t('common.working') : t('earn.submit')}
      </Button>
    </form>
  );
}
