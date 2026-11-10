import { useCallback, useEffect, useState } from 'react';
import type {
  CouponFilter,
  CouponInstance,
  CouponOffer,
  CouponTemplateSummary,
  MiniLoyal,
} from '@miniloyal/sdk';
import { MiniLoyalError } from '@miniloyal/sdk';

import type { Strings } from './i18n';

const FILTERS: CouponFilter[] = ['available', 'used', 'expired'];

/** Offers to claim, and my coupons by state with the code the cashier needs. */
export function CouponsScreen({ ml, t }: { ml: MiniLoyal; t: Strings }) {
  const [offers, setOffers] = useState<CouponOffer[]>([]);
  const [mine, setMine] = useState<CouponInstance[]>([]);
  const [filter, setFilter] = useState<CouponFilter>('available');
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [claiming, setClaiming] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const fail = useCallback((err: unknown) => setError(errorText(err, t)), [t]);

  const loadMine = useCallback(
    async (state: CouponFilter) => {
      setLoading(true);
      try {
        const page = await ml.coupons.list({ state, limit: 20 });
        setMine(page.items);
        setCursor(page.next_cursor ?? null);
      } catch (err) {
        fail(err);
      } finally {
        setLoading(false);
      }
    },
    [ml, fail],
  );

  useEffect(() => {
    ml.coupons.offers().then(setOffers, fail);
  }, [ml, fail]);

  useEffect(() => {
    void loadMine(filter);
  }, [filter, loadMine]);

  async function claim(offer: CouponOffer) {
    setClaiming(offer.template.id);
    setMessage(null);
    setError(null);
    try {
      const coupon = await ml.coupons.claim(offer.template.id);
      setMessage(`${t.claimed}: ${coupon.code}`);
      setOffers(await ml.coupons.offers());
      await loadMine(filter);
    } catch (err) {
      fail(err);
    } finally {
      setClaiming(null);
    }
  }

  async function more() {
    if (!cursor) return;
    setLoading(true);
    try {
      const page = await ml.coupons.list({ state: filter, limit: 20, cursor });
      setMine((prev) => [...prev, ...page.items]);
      setCursor(page.next_cursor ?? null);
    } catch (err) {
      fail(err);
    } finally {
      setLoading(false);
    }
  }

  const label: Record<CouponFilter, string> = {
    available: t.couponAvailable,
    used: t.couponUsed,
    expired: t.couponExpired,
  };

  return (
    <>
      <section className="card" aria-label={t.offers}>
        <h3>{t.offers}</h3>
        {message && <p role="status">{message}</p>}
        {error && <p role="alert">{error}</p>}
        {offers.length === 0 ? (
          <p className="muted">{t.offersEmpty}</p>
        ) : (
          <ul className="history">
            {offers.map((o) => (
              <li key={o.template.id}>
                <span>
                  {o.template.name}
                  <span className="muted"> · {describeValue(o.template, t)}</span>
                </span>
                <button
                  type="button"
                  onClick={() => void claim(o)}
                  disabled={!o.can_claim || claiming !== null}
                >
                  {o.can_claim ? t.claim : t.limitReached}
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="card" aria-label={t.myCoupons} aria-busy={loading}>
        <h3>{t.myCoupons}</h3>
        <nav className="tabs" role="tablist" aria-label={t.myCoupons}>
          {FILTERS.map((f) => (
            <button
              key={f}
              type="button"
              role="tab"
              aria-selected={filter === f}
              className={filter === f ? 'tab active' : 'tab'}
              onClick={() => setFilter(f)}
            >
              {label[f]}
            </button>
          ))}
        </nav>
        {mine.length === 0 && !loading ? (
          <p className="muted">{t.couponsEmpty}</p>
        ) : (
          <ul className="history">
            {mine.map((c) => (
              <li key={c.id}>
                <span>
                  {c.template.name}
                  <span className="muted"> · {describeValue(c.template, t)}</span>
                </span>
                <span>
                  <strong>{c.code}</strong>
                  <span className="muted">
                    {' '}
                    {c.expires_at ? `${t.expires} ${c.expires_at.slice(0, 10)}` : t.noExpiryDate}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        )}
        {cursor && (
          <button type="button" onClick={() => void more()} disabled={loading}>
            {loading ? t.loading : t.loadMore}
          </button>
        )}
      </section>
    </>
  );
}

function describeValue(tpl: CouponTemplateSummary, t: Strings): string {
  switch (tpl.type) {
    case 'amount_off':
      return t.amountOff.replace('{value}', String(tpl.value ?? 0));
    case 'percent_off':
      return t.percentOff.replace('{value}', String(tpl.value ?? 0));
    case 'free_item':
      return t.freeItem;
  }
}

function errorText(err: unknown, t: Strings): string {
  if (err instanceof MiniLoyalError) {
    switch (err.code) {
      case 'coupon_sold_out':
        return t.soldOut;
      case 'coupon_limit_reached':
        return t.limitReached;
      case 'coupon_not_claimable':
        return t.notClaimable;
    }
    return `${err.message} (${err.code})`;
  }
  return t.genericError;
}
