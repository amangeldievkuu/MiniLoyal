import { useCallback, useEffect, useState } from 'react';
import type { MiniLoyal, PointsKind, PointsSummary, PointsTransaction } from '@miniloyal/sdk';
import { MiniLoyalError } from '@miniloyal/sdk';

import type { Strings } from './i18n';

/** Balance, next expiry, the earn rule, and the history with "load more". */
export function PointsScreen({ ml, t }: { ml: MiniLoyal; t: Strings }) {
  const [summary, setSummary] = useState<PointsSummary | null>(null);
  const [items, setItems] = useState<PointsTransaction[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fail = useCallback(
    (err: unknown) =>
      setError(err instanceof MiniLoyalError ? `${err.message} (${err.code})` : t.genericError),
    [t],
  );

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    Promise.all([ml.points(), ml.pointsHistory({ limit: 20 })])
      .then(([s, page]) => {
        if (cancelled) return;
        setSummary(s);
        setItems(page.items);
        setCursor(page.next_cursor ?? null);
      })
      .catch((err: unknown) => {
        if (!cancelled) fail(err);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [ml, fail]);

  async function loadMore() {
    if (!cursor) return;
    setLoading(true);
    try {
      const page = await ml.pointsHistory({ limit: 20, cursor });
      setItems((prev) => [...prev, ...page.items]);
      setCursor(page.next_cursor ?? null);
    } catch (err) {
      fail(err);
    } finally {
      setLoading(false);
    }
  }

  const next = summary?.expiring[0];
  return (
    <>
      <section className="card" aria-label={t.tabPoints} aria-busy={loading}>
        {error && <p role="alert">{error}</p>}
        {summary ? (
          <>
            <p className="balance big">
              <strong>{summary.balance.toLocaleString()}</strong> {t.pointsUnit}
            </p>
            <p className="muted">
              {t.lifetimeEarned} {summary.lifetime_earned.toLocaleString()} {t.pointsUnit}
            </p>
            <p>
              {next
                ? `${t.nextExpiry}: ${next.points.toLocaleString()} ${t.pointsUnit} · ${next.expires_at.slice(0, 10)}`
                : t.noExpiry}
            </p>
            <p className="muted">
              {summary.rules.enabled
                ? t.earnRule
                    .replace('{spend}', summary.rules.spend_per_point.toLocaleString())
                    .replace('{points}', String(summary.rules.points_per_unit))
                : t.pointsDisabled}
            </p>
          </>
        ) : (
          !error && <p role="status">{t.loading}</p>
        )}
      </section>

      <section className="card" aria-label={t.history}>
        <h3>{t.history}</h3>
        {items.length === 0 && !loading ? (
          <p className="muted">{t.historyEmpty}</p>
        ) : (
          <ul className="history">
            {items.map((tx) => (
              <li key={tx.id}>
                <span>
                  {kindLabel(tx.kind, t)}
                  <span className="muted"> · {tx.created_at.slice(0, 10)}</span>
                </span>
                <span className={tx.points > 0 ? 'plus' : 'minus'}>
                  {tx.points > 0 ? '+' : ''}
                  {tx.points.toLocaleString()}
                  <span className="muted">
                    {' '}
                    ({t.balanceAfter} {tx.balance_after.toLocaleString()})
                  </span>
                </span>
              </li>
            ))}
          </ul>
        )}
        {cursor && (
          <button type="button" onClick={loadMore} disabled={loading}>
            {loading ? t.loading : t.loadMore}
          </button>
        )}
      </section>
    </>
  );
}

function kindLabel(kind: PointsKind, t: Strings): string {
  switch (kind) {
    case 'earn':
      return t.kindEarn;
    case 'redeem':
      return t.kindRedeem;
    case 'adjust':
      return t.kindAdjust;
    case 'expire':
      return t.kindExpire;
  }
}
