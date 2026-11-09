import { useMutation } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { QRCodeSVG } from 'qrcode.react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../api/client';
import type { PasswordChangeRequest, TotpEnrollment } from '../api/types';
import { sessionStore, useMe } from '../auth/session';
import { ErrorAlert, PageHeader, ShowOnceDialog } from '../components/feedback';
import { Alert, Badge, Button, Card, Field, Status } from '../components/ui';
import { SecurityActivityCard } from './SecurityActivityCard';

export function MePage() {
  const { t } = useTranslation();
  const me = useMe();
  const navigate = useNavigate();

  const changePassword = useMutation({
    mutationFn: async (body: PasswordChangeRequest) =>
      unwrap(await api.POST('/v1/dashboard/me/password', { body })),
    onSuccess: (session) => void sessionStore.start(session),
  });

  const logoutAll = useMutation({
    mutationFn: async () => unwrap(await api.POST('/v1/dashboard/me/logout-all')),
    onSuccess: () => {
      sessionStore.clear();
      void navigate({ to: '/login' });
    },
  });

  function submitPassword(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const form = e.currentTarget;
    changePassword.mutate(
      {
        current_password: String(fd.get('current_password') ?? ''),
        new_password: String(fd.get('new_password') ?? ''),
      },
      { onSuccess: () => form.reset() },
    );
  }

  const pwErr = asApiError(changePassword.error);

  if (!me) return <Status>{t('common.loading')}</Status>;

  return (
    <div className="space-y-6">
      <PageHeader title={t('me.title')} />
      <Card>
        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <dt className="text-zinc-500">{t('me.email')}</dt>
          <dd>{me.user.email}</dd>
          <dt className="text-zinc-500">{t('me.displayName')}</dt>
          <dd>{me.user.display_name ?? '—'}</dd>
        </dl>
      </Card>

      <Card title={t('me.password.title')}>
        <form
          onSubmit={submitPassword}
          className="grid gap-4 sm:grid-cols-2"
          aria-busy={changePassword.isPending}
        >
          <Field
            id="pw-current"
            name="current_password"
            type="password"
            autoComplete="current-password"
            label={t('me.password.current')}
            required
            error={pwErr?.field('current_password')}
          />
          <Field
            id="pw-new"
            name="new_password"
            type="password"
            autoComplete="new-password"
            label={t('me.password.new')}
            minLength={10}
            required
            hint={t('me.password.hint')}
            error={pwErr?.field('new_password')}
          />
          <div className="flex items-center gap-3 sm:col-span-2">
            <Button type="submit" disabled={changePassword.isPending}>
              {t('me.password.submit')}
            </Button>
            {changePassword.isSuccess && <Status>{t('me.password.done')}</Status>}
          </div>
          <div className="sm:col-span-2">
            <ErrorAlert error={changePassword.error} />
          </div>
        </form>
      </Card>

      <TotpCard enabled={me.user.totp_enabled} />

      <Card title={t('me.sessions.title')} description={t('me.sessions.logoutAllHelp')}>
        <Button variant="danger" onClick={() => logoutAll.mutate()} disabled={logoutAll.isPending}>
          {t('me.sessions.logoutAll')}
        </Button>
        <div className="mt-3">
          <ErrorAlert error={logoutAll.error} />
        </div>
      </Card>

      <SecurityActivityCard />
    </div>
  );
}

function TotpCard({ enabled }: { enabled: boolean }) {
  const { t } = useTranslation();
  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null);
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);

  const enroll = useMutation({
    mutationFn: async () => unwrap(await api.POST('/v1/dashboard/me/totp/enroll')),
    onSuccess: setEnrollment,
  });

  const confirm = useMutation({
    mutationFn: async (code: string) =>
      unwrap(await api.POST('/v1/dashboard/me/totp/confirm', { body: { code } })),
    onSuccess: (result) => {
      setEnrollment(null);
      setRecoveryCodes(result.recovery_codes);
      void sessionStore.reloadMe();
    },
  });

  const disable = useMutation({
    mutationFn: async (body: { password: string; code: string }) =>
      unwrap(await api.DELETE('/v1/dashboard/me/totp', { body })),
    onSuccess: () => void sessionStore.reloadMe(),
  });

  function submitConfirm(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    confirm.mutate(String(fd.get('code') ?? '').trim());
  }

  function submitDisable(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    disable.mutate({
      password: String(fd.get('password') ?? ''),
      code: String(fd.get('code') ?? '').trim(),
    });
  }

  const confirmErr = asApiError(confirm.error);
  const disableErr = asApiError(disable.error);

  return (
    <Card
      title={t('me.totp.title')}
      actions={
        <Badge tone={enabled ? 'success' : 'neutral'}>
          {enabled ? t('me.totp.enabled') : t('me.totp.disabled')}
        </Badge>
      }
    >
      {!enabled && !enrollment && (
        <div>
          <Button onClick={() => enroll.mutate()} disabled={enroll.isPending}>
            {t('me.totp.enroll')}
          </Button>
          <div className="mt-3">
            <ErrorAlert error={enroll.error} />
          </div>
        </div>
      )}

      {!enabled && enrollment && (
        <div className="grid gap-6 sm:grid-cols-[auto_1fr]">
          <div className="rounded-md bg-white p-3 shadow-xs">
            <QRCodeSVG value={enrollment.otpauth_url} size={176} />
          </div>
          <form onSubmit={submitConfirm} className="space-y-3" aria-busy={confirm.isPending}>
            <p className="text-sm">{t('me.totp.scan')}</p>
            <code className="block rounded bg-zinc-100 p-2 font-mono text-sm break-all dark:bg-zinc-800">
              {enrollment.secret}
            </code>
            <Field
              id="totp-confirm"
              name="code"
              inputMode="numeric"
              autoComplete="one-time-code"
              label={t('me.totp.confirmCode')}
              minLength={6}
              maxLength={8}
              required
              error={confirmErr?.field('code')}
            />
            <ErrorAlert error={confirm.error} />
            <div className="flex gap-2">
              <Button type="submit" disabled={confirm.isPending}>
                {t('me.totp.confirm')}
              </Button>
              <Button variant="ghost" onClick={() => setEnrollment(null)}>
                {t('common.cancel')}
              </Button>
            </div>
          </form>
        </div>
      )}

      {enabled && (
        <form
          onSubmit={submitDisable}
          className="grid gap-4 sm:grid-cols-2"
          aria-busy={disable.isPending}
        >
          <Field
            id="totp-disable-password"
            name="password"
            type="password"
            autoComplete="current-password"
            label={t('me.totp.disablePassword')}
            required
            error={disableErr?.field('password')}
          />
          <Field
            id="totp-disable-code"
            name="code"
            inputMode="numeric"
            autoComplete="one-time-code"
            label={t('me.totp.disableCode')}
            required
            error={disableErr?.field('code')}
          />
          <div className="sm:col-span-2">
            <Button type="submit" variant="danger" disabled={disable.isPending}>
              {t('me.totp.disableSubmit')}
            </Button>
          </div>
          <div className="sm:col-span-2">
            {disable.isSuccess && <Alert tone="success">{t('me.totp.disabledDone')}</Alert>}
            <ErrorAlert error={disable.error} />
          </div>
        </form>
      )}

      <ShowOnceDialog
        open={recoveryCodes !== null}
        title={t('me.totp.recoveryTitle')}
        help={t('me.totp.recoveryHelp')}
        value={recoveryCodes ?? []}
        onClose={() => setRecoveryCodes(null)}
      />
    </Card>
  );
}
