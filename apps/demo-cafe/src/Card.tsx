import { useEffect, useRef, useState } from 'react';
import QRCode from 'qrcode';
import type { MemberCard, MiniLoyal, PointsSummary } from '@miniloyal/sdk';

import type { Strings } from './i18n';

/**
 * The member card: a QR code that changes every 30 seconds, a ring that
 * counts down to the next code, and the points balance.
 */
export function CardScreen({ ml, t }: { ml: MiniLoyal; t: Strings }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [card, setCard] = useState<{ card: MemberCard; receivedAt: number } | null>(null);
  const [retrying, setRetrying] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [summary, setSummary] = useState<PointsSummary | null>(null);

  useEffect(() => {
    const stop = ml.watchCard(
      (c) => {
        setCard({ card: c, receivedAt: Date.now() });
        setRetrying(false);
      },
      () => setRetrying(true),
    );
    return stop;
  }, [ml]);

  useEffect(() => {
    let cancelled = false;
    ml.points()
      .then((s) => {
        if (!cancelled) setSummary(s);
      })
      .catch(() => {
        // The balance is a convenience on this screen; the Points tab shows errors.
      });
    return () => {
      cancelled = true;
    };
  }, [ml]);

  useEffect(() => {
    if (!card || !canvas.current) return;
    void QRCode.toCanvas(canvas.current, card.card.qr, { margin: 1, width: 240 });
  }, [card]);

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(id);
  }, []);

  const total = card ? Math.max(1, card.card.refresh_in_seconds) * 1000 : 1;
  const left = card ? Math.max(0, card.receivedAt + total - now) : 0;
  const fraction = card ? left / total : 0;

  return (
    <section className="card member-card" aria-label={t.tabCard}>
      <div className="qr-wrap">
        <canvas
          ref={canvas}
          role="img"
          aria-label={t.cardQrLabel}
          width={240}
          height={240}
          className={card ? '' : 'qr-placeholder'}
        />
        <Ring fraction={fraction} />
      </div>
      <p className="muted" aria-live="polite">
        {retrying
          ? t.cardRetrying
          : card
            ? `${Math.ceil(left / 1000)} ${t.cardRefreshIn}`
            : t.cardRefreshing}
      </p>
      <p>{t.cardHint}</p>
      {summary && (
        <p className="balance">
          <span className="muted">{t.balance}</span>{' '}
          <strong>
            {summary.balance.toLocaleString()} {t.pointsUnit}
          </strong>
        </p>
      )}
    </section>
  );
}

/** A countdown ring; fraction is the share of the window still left. */
function Ring({ fraction }: { fraction: number }) {
  const r = 18;
  const c = 2 * Math.PI * r;
  return (
    <svg className="ring" width={44} height={44} viewBox="0 0 44 44" aria-hidden="true">
      <circle cx={22} cy={22} r={r} className="ring-track" />
      <circle
        cx={22}
        cy={22}
        r={r}
        className="ring-fill"
        strokeDasharray={c}
        strokeDashoffset={c * (1 - fraction)}
        transform="rotate(-90 22 22)"
      />
    </svg>
  );
}
