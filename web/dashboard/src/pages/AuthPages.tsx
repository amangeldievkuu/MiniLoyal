import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import type { Session } from '../api/types';
import { sessionStore } from '../auth/session';
import { LANGUAGES, setLanguage, type Language } from '../i18n';
import { Card, Select } from '../components/ui';
import { LoginForm } from './auth/LoginForm';
import { RegisterForm } from './auth/RegisterForm';

/** Centered card with the brand and language switcher, for pages outside the shell. */
export function AuthLayout({ children, footer }: { children: ReactNode; footer?: ReactNode }) {
  const { t, i18n } = useTranslation();
  return (
    <div className="mx-auto flex min-h-screen w-full max-w-md flex-col justify-center px-4 py-8">
      <div className="mb-6 flex items-center justify-between">
        <span className="text-lg font-bold text-brand-700 dark:text-brand-500">{t('brand')}</span>
        <Select
          aria-label={t('nav.language')}
          className="w-auto"
          value={i18n.language}
          onChange={(e) => setLanguage(e.target.value as Language)}
        >
          {LANGUAGES.map((l) => (
            <option key={l.code} value={l.code}>
              {l.label}
            </option>
          ))}
        </Select>
      </div>
      <Card>{children}</Card>
      {footer && (
        <p className="mt-4 text-center text-sm text-zinc-600 dark:text-zinc-400">{footer}</p>
      )}
    </div>
  );
}

function safeRedirect(target: unknown): string {
  return typeof target === 'string' && target.startsWith('/') && !target.startsWith('//')
    ? target
    : '/';
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { redirect?: unknown };

  async function onLoggedIn(session: Session) {
    await sessionStore.start(session);
    await navigate({ to: safeRedirect(search.redirect) });
  }

  return (
    <AuthLayout
      footer={
        <>
          {t('auth.login.noAccount')}{' '}
          <Link
            to="/register"
            className="font-medium text-brand-700 hover:underline dark:text-brand-500"
          >
            {t('auth.login.register')}
          </Link>
        </>
      }
    >
      <LoginForm onLoggedIn={onLoggedIn} />
    </AuthLayout>
  );
}

export function RegisterPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();

  async function onRegistered(session: Session) {
    await sessionStore.start(session);
    await navigate({ to: '/' });
  }

  return (
    <AuthLayout
      footer={
        <>
          {t('auth.register.haveAccount')}{' '}
          <Link
            to="/login"
            className="font-medium text-brand-700 hover:underline dark:text-brand-500"
          >
            {t('auth.register.login')}
          </Link>
        </>
      }
    >
      <RegisterForm onRegistered={onRegistered} />
    </AuthLayout>
  );
}
