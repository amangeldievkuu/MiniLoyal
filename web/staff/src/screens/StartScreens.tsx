import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { start, submitInviteCode, type SessionState } from '../auth/session';
import { Alert, Button, Screen } from '../components/ui';

/** Seconds left of a wait, counting down once a second. */
function useCountdown(seconds: number | undefined): number {
  const [left, setLeft] = useState(seconds ?? 0);
  useEffect(() => {
    setLeft(seconds ?? 0);
    if (!seconds) return;
    const id = setInterval(() => setLeft((s) => Math.max(0, s - 1)), 1000);
    return () => clearInterval(id);
  }, [seconds]);
  return left;
}

/** Everything before the cashier is signed in, except the invite screen. */
export function StartScreen({
  state,
}: {
  state: Exclude<SessionState, { status: 'signed_in' | 'needs_invite' }>;
}) {
  const { t } = useTranslation();
  const wait = useCountdown(state.status === 'rate_limited' ? state.retryAfterSeconds : undefined);

  if (state.status === 'starting' || state.status === 'redirecting') {
    return (
      <Screen>
        <p role="status" aria-live="polite" className="text-center text-lg">
          {state.status === 'starting' ? t('start.loading') : t('start.redirecting')}
        </p>
      </Screen>
    );
  }

  let message: string;
  let canRetry = true;
  let tone: 'danger' | 'warning' | 'neutral' = 'danger';
  switch (state.status) {
    case 'rate_limited':
      message = t('start.rateLimited', { seconds: wait });
      tone = 'warning';
      canRetry = wait === 0;
      break;
    case 'signed_out':
      message = state.reason === 'expired' ? t('start.expired') : t('start.signedOut');
      tone = 'neutral';
      break;
    case 'error':
      message = {
        not_configured: t('start.notConfigured'),
        liff_init: t('start.liffInit'),
        no_id_token: t('start.noIdToken'),
        app_not_found: t('start.appNotFound'),
        staff_channel_required: t('start.staffChannelRequired'),
        server: t('start.server'),
      }[state.reason];
      canRetry = state.reason !== 'not_configured' && state.reason !== 'staff_channel_required';
      break;
  }

  return (
    <Screen>
      <h1 className="text-2xl font-bold">{t('app.title')}</h1>
      <Alert tone={tone}>{message}</Alert>
      {canRetry && (
        <Button size="lg" onClick={() => void start()}>
          {state.status === 'signed_out' ? t('start.signInAgain') : t('start.tryAgain')}
        </Button>
      )}
    </Screen>
  );
}

/** First sign-in: the cashier types the invite code from the dashboard. */
export function InviteScreen({
  state,
}: {
  state: Extract<SessionState, { status: 'needs_invite' }>;
}) {
  const { t } = useTranslation();
  const [code, setCode] = useState('');
  const [pending, setPending] = useState(false);
  const wait = useCountdown(state.error === 'rate_limited' ? state.retryAfterSeconds : undefined);

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (pending || !code.trim()) return;
    setPending(true);
    void submitInviteCode(code).finally(() => setPending(false));
  }

  const error =
    state.error === 'wrong_code'
      ? t('invite.wrongCode')
      : state.error === 'rate_limited'
        ? t('invite.rateLimited', { seconds: wait })
        : state.error === 'network'
          ? t('invite.network')
          : null;
  const blocked = state.error === 'rate_limited' && wait > 0;

  return (
    <Screen>
      <form onSubmit={submit} className="flex flex-col gap-4" aria-busy={pending}>
        <h1 className="text-2xl font-bold">{t('invite.title')}</h1>
        <p className="text-base text-zinc-700 dark:text-zinc-300">{t('invite.help')}</p>
        <label htmlFor="invite-code" className="text-base font-semibold">
          {t('invite.label')}
        </label>
        <input
          id="invite-code"
          name="invite_code"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          autoComplete="one-time-code"
          autoCapitalize="characters"
          spellCheck={false}
          maxLength={32}
          aria-describedby="invite-hint"
          placeholder="XXXX-XXXX-XXXX"
          className="min-h-14 rounded-xl border-2 border-zinc-400 bg-white px-4 font-mono text-2xl tracking-widest uppercase dark:border-zinc-600 dark:bg-zinc-900"
        />
        <p id="invite-hint" className="text-sm text-zinc-600 dark:text-zinc-400">
          {t('invite.hint')}
        </p>
        {error && (
          <Alert tone={state.error === 'rate_limited' ? 'warning' : 'danger'}>{error}</Alert>
        )}
        <Button type="submit" size="lg" disabled={pending || blocked || !code.trim()}>
          {pending ? t('common.working') : t('invite.submit')}
        </Button>
      </form>
    </Screen>
  );
}
