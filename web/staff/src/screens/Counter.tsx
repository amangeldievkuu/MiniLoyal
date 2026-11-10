import { useCallback, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, toApiError, unwrap } from '../api/client';
import type { PointsTransaction, ScannedMember, ScanResult, StaffSession } from '../api/types';
import { CameraScanner } from '../components/CameraScanner';
import { MemberPanel } from '../components/MemberPanel';
import { Alert, Button, Card } from '../components/ui';
import {
  sendPointsWrite as send,
  setPendingAction,
  usePendingAction,
  type Outcome,
  type PendingAction,
  type PointsWrite,
} from '../lib/pending';
import { canScanNatively, liff } from '../liff';
import { describeActionError } from './actionError';
import { EarnForm } from './EarnForm';
import { RedeemForm } from './RedeemForm';

type Stage =
  | { name: 'idle' }
  | { name: 'camera' }
  | { name: 'member'; scan: ScanResult; tab: 'earn' | 'redeem' }
  | { name: 'done'; txn: PointsTransaction; member: ScannedMember };

interface Notice {
  tone: 'warning' | 'danger';
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
      return send({ ...w, key: crypto.randomUUID(), staffId, member });
    },
    [staffId],
  );

  /** Resends the pending write with the same key and body. */
  async function retry() {
    if (!pending || retrying) return;
    setRetrying(true);
    setConfirmDiscard(false);
    try {
      const out = await send(pending);
      if (out.ok) {
        setNotice(null);
        setStage({ name: 'done', txn: out.txn, member: pending.member });
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
        setStage({ name: 'member', scan, tab: 'earn' });
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
            {(['earn', 'redeem'] as const).map((tab) => (
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
                {tab === 'earn' ? t('member.earn') : t('member.redeem')}
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
  const what =
    pending.kind === 'redeem'
      ? t('pending.redeem', { name, points: pending.body.points.toLocaleString() })
      : 'spend_amount' in pending.body
        ? t('pending.earnSpend', { name, amount: pending.body.spend_amount.toLocaleString() })
        : t('pending.earnPoints', { name, points: pending.body.points.toLocaleString() });
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
