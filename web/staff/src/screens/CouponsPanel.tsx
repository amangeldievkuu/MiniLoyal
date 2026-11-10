import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import type { ApiError } from '../api/client';
import type { CouponInstance, ScanResult } from '../api/types';
import { Alert, Button, Card } from '../components/ui';
import type { WriteOutcome } from '../lib/pending';
import { describeActionError } from './actionError';
import { couponValue, formatDate } from './format';

/**
 * The member's available coupons. Redeeming one asks for confirmation and
 * does not use up the scan, so several can be redeemed on one scan. The
 * counter owns the request (key and body live there until the API answers).
 */
export function CouponsPanel({
  scan,
  redeemed,
  blocked,
  onRedeem,
  onScanAgain,
}: {
  scan: ScanResult;
  /** Coupons redeemed on this scan, as the API returned them. */
  redeemed: CouponInstance[];
  blocked: boolean;
  onRedeem: (coupon: CouponInstance) => Promise<WriteOutcome>;
  onScanAgain: () => void;
}) {
  const { t } = useTranslation();
  const [asking, setAsking] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<{ id: string; err: ApiError } | null>(null);
  const failure = error ? describeActionError(error.err, t, 0) : null;

  function confirm(coupon: CouponInstance) {
    if (busy || blocked) return;
    setBusy(coupon.id);
    setError(null);
    void onRedeem(coupon)
      .then((out) => {
        // An unconfirmed write is handled by the retry banner; close the question.
        if (out.ok || out.ambiguous) setAsking(null);
        else setError({ id: coupon.id, err: out.error });
      })
      .finally(() => setBusy(null));
  }

  return (
    <div className="flex flex-col gap-3" aria-busy={busy !== null}>
      {blocked && <Alert tone="warning">{t('pending.blocked')}</Alert>}
      {redeemed.map((c) => (
        <Alert key={c.id} tone="success">
          <p className="font-semibold">{t('coupons.redeemedDone', { name: c.template.name })}</p>
        </Alert>
      ))}
      {scan.coupons.length === 0 && redeemed.length === 0 && (
        <p className="py-2 text-base">{t('coupons.none')}</p>
      )}
      <ul className="flex flex-col gap-3">
        {scan.coupons.map((c) => (
          <li key={c.id}>
            <Card className="flex flex-col gap-2">
              <div>
                <p className="text-lg font-semibold">{c.template.name}</p>
                <p className="text-base">{couponValue(c.template, t)}</p>
                <p className="text-sm">
                  {c.expires_at
                    ? t('coupons.expires', { date: formatDate(c.expires_at) })
                    : t('coupons.noExpiry')}
                </p>
              </div>
              {error?.id === c.id && failure && (
                <Alert tone="danger">
                  <p>{failure.message}</p>
                  {failure.scanAgain && (
                    <Button variant="secondary" className="mt-2" onClick={onScanAgain}>
                      {t('scan.button')}
                    </Button>
                  )}
                </Alert>
              )}
              {asking === c.id ? (
                <div className="flex flex-col gap-2">
                  <p className="font-semibold">
                    {t('coupons.confirmBody', { name: c.template.name })}
                  </p>
                  <div className="grid grid-cols-2 gap-2">
                    <Button variant="secondary" onClick={() => setAsking(null)} disabled={!!busy}>
                      {t('common.back')}
                    </Button>
                    <Button
                      onClick={() => confirm(c)}
                      disabled={!!busy || blocked || failure?.scanAgain}
                    >
                      {busy === c.id ? t('common.working') : t('coupons.confirm')}
                    </Button>
                  </div>
                </div>
              ) : (
                <Button
                  onClick={() => {
                    setError(null);
                    setAsking(c.id);
                  }}
                  disabled={!!busy || blocked}
                  aria-label={t('coupons.redeemNamed', { name: c.template.name })}
                >
                  {t('coupons.redeem')}
                </Button>
              )}
            </Card>
          </li>
        ))}
      </ul>
    </div>
  );
}
