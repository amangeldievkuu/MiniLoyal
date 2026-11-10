import { useCallback, useState, type FormEvent } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';

import { api, toApiError, unwrap } from '../api/client';
import type {
  CouponInstance,
  PointsTransaction,
  ScannedMember,
  ScanResult,
  StampProgress,
  StampResult,
  StaffSession,
} from '../api/types';
import { CameraScanner } from '../components/CameraScanner';
import { MemberPanel } from '../components/MemberPanel';
import { Alert, Button, Card } from '../components/ui';
import {
  sendPointsWrite,
  sendWrite as send,
  setPendingAction,
  usePendingAction,
  type Applied,
  type CouponWrite,
  type Outcome,
  type PendingAction,
  type PointsWrite,
  type StampWrite,
  type WriteOutcome,
} from '../lib/pending';
import { canScanNatively, liff } from '../liff';
import { describeActionError } from './actionError';
import { CouponsPanel } from './CouponsPanel';
import { EarnForm } from './EarnForm';
import { RedeemForm } from './RedeemForm';
import { StampsPanel } from './StampsPanel';

type Tab = 'earn' | 'redeem' | 'coupons' | 'stamps';
const TABS: Tab[] = ['earn', 'redeem', 'coupons', 'stamps'];

type Stage =
  | { name: 'idle' }
  | { name: 'camera' }
  | {
      name: 'member';
      scan: ScanResult;
      tab: Tab;
      /** Coupons redeemed on this scan, as the API returned them. */
      redeemed: CouponInstance[];
      /** Stamps given per card on this scan (the per-visit cap counts them). */
      given: Record<string, number>;
      results: Record<string, StampResult>;
    }
  | { name: 'done'; txn: PointsTransaction; member: ScannedMember };

interface Notice {
  tone: 'warning' | 'danger' | 'success';
  text: string;
}

/** The cashier's loop: scan a card, add or redeem points, next customer. */
export function Counter({ session }: { session: StaffSession }) {
  const { t } = useTranslation();
  const [stage, setStage] = useState<Stage>({ name: 'idle' });
  const [notice, setNotice] = useState<Notice | null>(null);
  const [scanning, setScanning] = useState(false);
  const [manual, setManual] = useState('');
  const [retrying, setRetrying] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const isManager = session.staff.role === 'manager';
  const staffId = session.staff.id;
  const pending = usePendingAction(staffId);

  /** A new write from a form: a fresh key, held until the outcome is known. */
  const submitWrite = useCallback(
    (w: PointsWrite, member: ScannedMember): Promise<Outcome> => {
      setConfirmDiscard(false);
      return sendPointsWrite({ ...w, key: crypto.randomUUID(), staffId, member });
    },
    [staffId],
  );

  /** Applies a coupon or stamp answer to the member panel, if it is still open. */
  const applyApplied = useCallback((applied: Applied, memberId: string) => {
    setStage((prev) => {
      if (prev.name !== 'member' || prev.scan.member.id !== memberId) return prev;
      if (applied.kind === 'coupon') {
        const c = applied.coupon;
        return {
          ...prev,
          scan: { ...prev.scan, coupons: prev.scan.coupons.filter((x) => x.id !== c.id) },
          redeemed: [...prev.redeemed, c],
        };
      }
      if (applied.kind === 'stamp') {
        const r = applied.result;
        const id = r.progress.stamp_card_id;
        return {
          ...prev,
          scan: {
            ...prev.scan,
            stamps: prev.scan.stamps.map((s) => (s.stamp_card_id === id ? r.progress : s)),
          },
          given: { ...prev.given, [id]: (prev.given[id] ?? 0) + r.added },
          results: { ...prev.results, [id]: r },
        };
      }
      return prev;
    });
  }, []);

  const submitCoupon = useCallback(
    async (w: CouponWrite, member: ScannedMember): Promise<WriteOutcome> => {
      setConfirmDiscard(false);
      const out = await send({ ...w, key: crypto.randomUUID(), staffId, member });
      if (out.ok) applyApplied(out.applied, member.id);
      return out;
    },
    [staffId, applyApplied],
  );

  const submitStamp = useCallback(
    async (w: StampWrite, member: ScannedMember): Promise<WriteOutcome> => {
      setConfirmDiscard(false);
      const out = await send({ ...w, key: crypto.randomUUID(), staffId, member });
      if (out.ok) applyApplied(out.applied, member.id);
      return out;
    },
    [staffId, applyApplied],
  );

  /** Resends the pending write with the same key and body. */
  async function retry() {
    if (!pending || retrying) return;
    setRetrying(true);
    setConfirmDiscard(false);
    try {
      const out = await send(pending);
      if (out.ok) {
        if (out.applied.kind === 'points') {
          setNotice(null);
          setStage({ name: 'done', txn: out.applied.txn, member: pending.member });
        } else {
          applyApplied(out.applied, pending.member.id);
          setNotice({ tone: 'success', text: t('pending.applied') });
        }
      } else if (!out.ambiguous) {
        const failure = describeActionError(out.error, t, 0);
        setNotice({ tone: 'danger', text: t('pending.notApplied', { reason: failure.message }) });
      }
    } finally {
      setRetrying(false);
    }
  }

  function discard() {
    setPendingAction(null);
    setConfirmDiscard(false);
    setNotice({ tone: 'warning', text: t('pending.discarded') });
  }

  const blockedFor = (memberId: string) => pending?.member.id === memberId;

  const lookUp = useCallback(
    async (qr: string) => {
      setScanning(true);
      setNotice(null);
      try {
        const scan = unwrap(await api.POST('/v1/staff/scan', { body: { qr: qr.trim() } }));
        setManual('');
        setStage({
          name: 'member',
          scan,
          tab: 'earn',
          redeemed: [],
          given: {},
          results: {},
        });
      } catch (err) {
        const e = toApiError(err);
        const text =
          e.code === 'card_expired'
            ? t('scan.errors.cardExpired')
            : e.code === 'invalid_card' || e.status === 422
              ? t('scan.errors.invalidCard')
              : t('scan.errors.generic');
        setNotice({ tone: 'danger', text });
        setStage({ name: 'idle' });
      } finally {
        setScanning(false);
      }
    },
    [t],
  );

  function startScan() {
    setNotice(null);
    if (!canScanNatively()) {
      setStage({ name: 'camera' });
      return;
    }
    void liff
      .scanCodeV2()
      .then((r) => {
        if (r.value) void lookUp(r.value);
      })
      .catch(() => setNotice({ tone: 'danger', text: t('scan.errors.generic') }));
  }

  const onCameraResult = useCallback((text: string) => void lookUp(text), [lookUp]);
  const onExpire = useCallback(() => {
    setStage({ name: 'idle' });
    setNotice({ tone: 'warning', text: t('member.expired') });
  }, [t]);
  const scanAgain = useCallback(() => {
    setStage({ name: 'idle' });
    setNotice({ tone: 'warning', text: t('errors.scanAgain') });
  }, [t]);

  function submitManual(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (manual.trim()) void lookUp(manual);
  }

  return (
    <main className="mx-auto flex w-full max-w-md flex-col gap-4 px-4 py-4">
      {pending && (
        <PendingBanner
          pending={pending}
          retrying={retrying}
          confirmDiscard={confirmDiscard}
          onRetry={() => void retry()}
          onAskDiscard={() => setConfirmDiscard(true)}
          onDiscard={discard}
          onKeep={() => setConfirmDiscard(false)}
        />
      )}
      {notice && <Alert tone={notice.tone}>{notice.text}</Alert>}

      {stage.name === 'idle' && (
        <>
          <Button size="lg" className="min-h-24 text-2xl" onClick={startScan} disabled={scanning}>
            {scanning ? t('scan.scanning') : t('scan.button')}
          </Button>
          <details className="rounded-xl border border-zinc-200 p-3 dark:border-zinc-800">
            <summary className="flex min-h-12 cursor-pointer items-center text-base">
              {t('scan.typeCode')}
            </summary>
            <form onSubmit={submitManual} className="mt-2 flex flex-col gap-2">
              <label htmlFor="manual-code" className="text-sm font-semibold">
                {t('scan.codeLabel')}
              </label>
              <input
                id="manual-code"
                value={manual}
                onChange={(e) => setManual(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                className="min-h-12 rounded-xl border-2 border-zinc-400 bg-white px-3 font-mono dark:border-zinc-600 dark:bg-zinc-900"
              />
              <Button type="submit" variant="secondary" disabled={scanning || !manual.trim()}>
                {t('scan.submitCode')}
              </Button>
            </form>
          </details>
        </>
      )}

      {stage.name === 'camera' && (
        <CameraScanner onResult={onCameraResult} onCancel={() => setStage({ name: 'idle' })} />
      )}

      {stage.name === 'member' && (
        <>
          <MemberPanel
            member={stage.scan.member}
            balance={stage.scan.balance}
            expiresAt={stage.scan.expires_at}
            onExpire={onExpire}
          />
          <div role="tablist" className="grid grid-cols-2 gap-2">
            {TABS.map((tab) => (
              <button
                key={tab}
                role="tab"
                type="button"
                aria-selected={stage.tab === tab}
                onClick={() => setStage({ ...stage, tab })}
                className={`min-h-12 rounded-xl text-lg font-semibold ${
                  stage.tab === tab
                    ? 'bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900'
                    : 'bg-zinc-200 dark:bg-zinc-800'
                }`}
              >
                {t(`member.tabs.${tab}`)}
              </button>
            ))}
          </div>
          <div role="tabpanel">
            {stage.tab === 'earn' ? (
              <EarnForm
                key={stage.scan.scan_token}
                scan={stage.scan}
                isManager={isManager}
                blocked={blockedFor(stage.scan.member.id)}
                onSubmit={(body) => submitWrite({ kind: 'earn', body }, stage.scan.member)}
                onDone={(txn) => setStage({ name: 'done', txn, member: stage.scan.member })}
                onScanAgain={scanAgain}
              />
            ) : stage.tab === 'coupons' ? (
              <CouponsPanel
                scan={stage.scan}
                redeemed={stage.redeemed}
                blocked={blockedFor(stage.scan.member.id)}
                onRedeem={(c) =>
                  submitCoupon(
                    {
                      kind: 'coupon',
                      couponId: c.id,
                      couponName: c.template.name,
                      body: { scan_token: stage.scan.scan_token },
                    },
                    stage.scan.member,
                  )
                }
                onScanAgain={scanAgain}
              />
            ) : stage.tab === 'stamps' ? (
              <StampsPanel
                scan={stage.scan}
                given={stage.given}
                results={stage.results}
                blocked={blockedFor(stage.scan.member.id)}
                onAdd={(card: StampProgress, count) =>
                  submitStamp(
                    {
                      kind: 'stamp',
                      cardName: card.name,
                      body: {
                        scan_token: stage.scan.scan_token,
                        stamp_card_id: card.stamp_card_id,
                        count,
                      },
                    },
                    stage.scan.member,
                  )
                }
                onScanAgain={scanAgain}
              />
            ) : (
              <RedeemForm
                key={stage.scan.scan_token}
                scan={stage.scan}
                blocked={blockedFor(stage.scan.member.id)}
                onSubmit={(body) => submitWrite({ kind: 'redeem', body }, stage.scan.member)}
                onDone={(txn) => setStage({ name: 'done', txn, member: stage.scan.member })}
                onScanAgain={scanAgain}
              />
            )}
          </div>
          <Button variant="ghost" onClick={() => setStage({ name: 'idle' })}>
            {t('member.clear')}
          </Button>
        </>
      )}

      {stage.name === 'done' && (
        <Card className="flex flex-col items-center gap-3 text-center">
          <div role="status" aria-live="polite" className="flex flex-col gap-2">
            <p className="text-lg">{stage.member.display_name ?? t('member.anonymous')}</p>
            <p className="text-3xl font-bold">
              {stage.txn.points >= 0
                ? t('result.earned', { points: stage.txn.points.toLocaleString() })
                : t('result.redeemed', { points: Math.abs(stage.txn.points).toLocaleString() })}
            </p>
            <p className="text-xl">
              {t('result.newBalance', { balance: stage.txn.balance_after.toLocaleString() })}
            </p>
          </div>
          <Button
            size="lg"
            className="w-full"
            onClick={() => {
              setNotice(null);
              setStage({ name: 'idle' });
            }}
          >
            {t('result.next')}
          </Button>
        </Card>
      )}
    </main>
  );
}

/**
 * The write whose answer was lost. It stays on screen through new scans and
 * expired panels until a retry settles it or the cashier discards it.
 */
function PendingBanner({
  pending,
  retrying,
  confirmDiscard,
  onRetry,
  onAskDiscard,
  onDiscard,
  onKeep,
}: {
  pending: PendingAction;
  retrying: boolean;
  confirmDiscard: boolean;
  onRetry: () => void;
  onAskDiscard: () => void;
  onDiscard: () => void;
  onKeep: () => void;
}) {
  const { t } = useTranslation();
  const name = pending.member.display_name ?? t('member.anonymous');
  const what = describePending(pending, name, t);
  const reason = pending.error ? describeActionError(pending.error, t, 0).message : null;

  return (
    <section aria-labelledby="pending-title" aria-busy={retrying}>
      <Alert tone="danger">
        <h2 id="pending-title" className="font-semibold">
          {t('pending.title')}
        </h2>
        <p>{what}</p>
        {reason && <p>{reason}</p>}
        <p className="mt-1 text-sm">{t('pending.help')}</p>
        {confirmDiscard ? (
          <div className="mt-3 flex flex-col gap-2">
            <p className="font-semibold">{t('pending.discardConfirm')}</p>
            <div className="grid grid-cols-2 gap-2">
              <Button variant="secondary" onClick={onKeep}>
                {t('pending.keep')}
              </Button>
              <Button variant="secondary" onClick={onDiscard}>
                {t('pending.discardYes')}
              </Button>
            </div>
          </div>
        ) : (
          <div className="mt-3 grid grid-cols-2 gap-2">
            <Button onClick={onRetry} disabled={retrying}>
              {retrying ? t('common.working') : t('common.retry')}
            </Button>
            <Button variant="secondary" onClick={onAskDiscard} disabled={retrying}>
              {t('pending.discard')}
            </Button>
          </div>
        )}
      </Alert>
    </section>
  );
}

function describePending(p: PendingAction, name: string, t: TFunction): string {
  switch (p.kind) {
    case 'coupon':
      return t('pending.coupon', { name, coupon: p.couponName });
    case 'stamp':
      return t('pending.stamp', { name, card: p.cardName, count: p.body.count });
    case 'redeem':
      return t('pending.redeem', { name, points: p.body.points.toLocaleString() });
    case 'earn':
      return 'spend_amount' in p.body
        ? t('pending.earnSpend', { name, amount: p.body.spend_amount.toLocaleString() })
        : t('pending.earnPoints', { name, points: p.body.points.toLocaleString() });
  }
}
