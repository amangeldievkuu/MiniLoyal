import { useEffect, useState } from 'react';
import type { MiniLoyal, StampProgress } from '@miniloyal/sdk';
import { MiniLoyalError } from '@miniloyal/sdk';

import type { Strings } from './i18n';

/** Progress per stamp card, with the reward. */
export function StampsScreen({ ml, t }: { ml: MiniLoyal; t: Strings }) {
  const [cards, setCards] = useState<StampProgress[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    ml.stamps().then(
      (c) => {
        if (!cancelled) setCards(c);
      },
      (err: unknown) => {
        if (!cancelled)
          setError(err instanceof MiniLoyalError ? `${err.message} (${err.code})` : t.genericError);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [ml, t]);

  return (
    <section className="card" aria-label={t.tabStamps} aria-busy={cards === null && !error}>
      {error && <p role="alert">{error}</p>}
      {cards === null ? (
        !error && <p role="status">{t.loading}</p>
      ) : cards.length === 0 ? (
        <p className="muted">{t.stampsEmpty}</p>
      ) : (
        cards.map((c) => (
          <div key={c.stamp_card_id}>
            <h3>{c.name}</h3>
            <p aria-label={`${c.count} / ${c.stamps_required}`}>
              {Array.from({ length: c.stamps_required }, (_, i) => (i < c.count ? '●' : '○')).join(
                ' ',
              )}
            </p>
            <p className="muted">
              {c.count} / {c.stamps_required} · {t.completions} {c.completions}
            </p>
            {c.reward && (
              <p>
                {t.reward}: {c.reward.name}
              </p>
            )}
            {c.unrewarded_completions > 0 && <p className="muted">{t.rewardPending}</p>}
          </div>
        ))
      )}
    </section>
  );
}
