import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../../api/client';
import type { Session } from '../../api/types';
import { ErrorAlert } from '../../components/feedback';
import { Button, Field } from '../../components/ui';

type Step = { kind: 'credentials' } | { kind: 'mfa'; token: string };

/** Email and password, then the MFA code when the account requires it. */
export function LoginForm({
  onLoggedIn,
}: {
  onLoggedIn: (session: Session) => Promise<void> | void;
}) {
  const { t } = useTranslation();
  const [step, setStep] = useState<Step>({ kind: 'credentials' });
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function run(fn: () => Promise<void>) {
    setPending(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  function submitCredentials(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    void run(async () => {
      const result = unwrap(
        await api.POST('/v1/dashboard/auth/login', {
          body: {
            email: String(fd.get('email') ?? ''),
            password: String(fd.get('password') ?? ''),
          },
        }),
      );
      if (result.session) await onLoggedIn(result.session);
      else if (result.mfa) setStep({ kind: 'mfa', token: result.mfa.mfa_token });
      else throw new Error('login returned neither a session nor a challenge');
    });
  }

  function submitMfa(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (step.kind !== 'mfa') return;
    const fd = new FormData(e.currentTarget);
    const token = step.token;
    void run(async () => {
      const session = unwrap(
        await api.POST('/v1/dashboard/auth/mfa', {
          body: { mfa_token: token, code: String(fd.get('code') ?? '').trim() },
        }),
      );
      await onLoggedIn(session);
    });
  }

  const apiError = asApiError(error);

  if (step.kind === 'mfa') {
    // Distinct keys remount the form, so the code field never inherits the password input's DOM value.
    return (
      <form key="mfa" onSubmit={submitMfa} className="space-y-4" aria-busy={pending}>
        <h1 className="text-xl font-semibold">{t('auth.mfa.title')}</h1>
        <p className="text-sm text-zinc-600 dark:text-zinc-400">{t('auth.mfa.help')}</p>
        <Field
          id="mfa-code"
          name="code"
          label={t('auth.mfa.code')}
          inputMode="numeric"
          autoComplete="one-time-code"
          required
          autoFocus
          error={apiError?.field('code')}
        />
        <ErrorAlert error={error} />
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={pending}>
            {pending ? t('common.working') : t('auth.mfa.submit')}
          </Button>
          <Button variant="ghost" onClick={() => setStep({ kind: 'credentials' })}>
            {t('auth.mfa.back')}
          </Button>
        </div>
      </form>
    );
  }

  return (
    <form key="credentials" onSubmit={submitCredentials} className="space-y-4" aria-busy={pending}>
      <h1 className="text-xl font-semibold">{t('auth.login.title')}</h1>
      <Field
        id="login-email"
        name="email"
        type="email"
        label={t('auth.login.email')}
        autoComplete="username"
        required
        error={apiError?.field('email')}
      />
      <Field
        id="login-password"
        name="password"
        type="password"
        label={t('auth.login.password')}
        autoComplete="current-password"
        required
        error={apiError?.field('password')}
      />
      <ErrorAlert error={error} />
      <Button type="submit" className="w-full" disabled={pending}>
        {pending ? t('common.working') : t('auth.login.submit')}
      </Button>
    </form>
  );
}
