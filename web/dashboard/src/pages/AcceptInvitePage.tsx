import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import type { Session } from '../api/types';
import { sessionStore } from '../auth/session';
import { ErrorAlert } from '../components/feedback';
import { Alert, Button, Field } from '../components/ui';
import { AuthLayout } from './AuthPages';

/** Landing page of an invitation link: /accept-invite?token=... */
export function AcceptInvitePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { token?: unknown };
  const token = typeof search.token === 'string' ? search.token : '';

  async function onAccepted(session: Session) {
    await sessionStore.start(session);
    await navigate({ to: '/' });
  }

  return (
    <AuthLayout
      footer={
        <Link
          to="/login"
          className="font-medium text-brand-700 hover:underline dark:text-brand-500"
        >
          {t('auth.acceptInvite.backToLogin')}
        </Link>
      }
    >
      {token ? (
        <AcceptInviteForm token={token} onAccepted={onAccepted} />
      ) : (
        <Alert tone="danger">{t('auth.acceptInvite.missingToken')}</Alert>
      )}
    </AuthLayout>
  );
}

/**
 * Password and display name for the invitee. The API creates the account
 * when the email has none (password required) or checks the password of an
 * existing one, then adds the membership and signs the user in.
 */
export function AcceptInviteForm({
  token,
  onAccepted,
}: {
  token: string;
  onAccepted: (session: Session) => Promise<void> | void;
}) {
  const { t } = useTranslation();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const password = String(fd.get('password') ?? '');
    const displayName = String(fd.get('display_name') ?? '').trim();
    setPending(true);
    setError(null);
    void (async () => {
      try {
        const session = unwrap(
          await api.POST('/v1/dashboard/auth/accept-invite', {
            body: {
              token,
              ...(password ? { password } : {}),
              ...(displayName ? { display_name: displayName } : {}),
            },
          }),
        );
        await onAccepted(session);
      } catch (err) {
        setError(err);
      } finally {
        setPending(false);
      }
    })();
  }

  const apiError = asApiError(error);

  return (
    <form onSubmit={submit} className="space-y-4" aria-busy={pending}>
      <h1 className="text-xl font-semibold">{t('auth.acceptInvite.title')}</h1>
      <p className="text-sm text-zinc-600 dark:text-zinc-400">{t('auth.acceptInvite.help')}</p>
      <Field
        id="invite-password"
        name="password"
        type="password"
        label={t('auth.acceptInvite.password')}
        autoComplete="new-password"
        minLength={10}
        hint={t('auth.acceptInvite.passwordHint')}
        error={apiError?.field('password')}
      />
      <Field
        id="invite-display-name"
        name="display_name"
        label={`${t('auth.acceptInvite.displayName')} (${t('common.optional')})`}
        autoComplete="name"
        maxLength={100}
        error={apiError?.field('display_name')}
      />
      <ErrorAlert error={error} />
      <Button type="submit" className="w-full" disabled={pending}>
        {pending ? t('common.working') : t('auth.acceptInvite.submit')}
      </Button>
    </form>
  );
}
