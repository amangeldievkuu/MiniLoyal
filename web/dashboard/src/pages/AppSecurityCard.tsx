import { useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { api, unwrap } from '../api/client';
import { useIdempotentMutation } from '../api/mutations';
import { keys } from '../api/queries';
import type { App } from '../api/types';
import { ErrorAlert } from '../components/feedback';
import { Alert, Button, Card, Dialog, Field } from '../components/ui';
import { formatDateTime } from '../lib/format';

/** The phrase that confirms signing every member out; the same in every language. */
export const SIGN_OUT_PHRASE = 'SIGN OUT';

/** HS256 opt-in and "sign out all members" for one app. */
export function AppSecurityCard({ app, editable }: { app: App; editable: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [phrase, setPhrase] = useState('');
  const refresh = (updated: App) => {
    qc.setQueryData(keys.app(app.id), updated);
    void qc.invalidateQueries({ queryKey: keys.apps(app.tenant_id) });
  };

  const toggle = useIdempotentMutation(
    async (allow: boolean, key: string) =>
      unwrap(
        await api.PATCH('/v1/dashboard/apps/{appId}', {
          params: { path: { appId: app.id }, header: { 'Idempotency-Key': key } },
          body: { allow_hs256_id_tokens: allow },
        }),
      ),
    { onSuccess: refresh },
  );

  const signOut = useIdempotentMutation(
    async (_: undefined, key: string) =>
      unwrap(
        await api.POST('/v1/dashboard/apps/{appId}/member-sessions/end', {
          params: { path: { appId: app.id }, header: { 'Idempotency-Key': key } },
        }),
      ),
    {
      onSuccess: (updated) => {
        refresh(updated);
        close();
      },
    },
  );

  function close() {
    setConfirming(false);
    setPhrase('');
    signOut.reset();
  }

  const hs256 = app.allow_hs256_id_tokens;

  return (
    <Card title={t('appPage.security.title')}>
      <div className="space-y-6">
        <section className="space-y-2">
          <label className="flex items-center gap-2 text-sm font-medium">
            <input
              type="checkbox"
              checked={hs256}
              disabled={!editable || toggle.isPending}
              onChange={(e) => toggle.mutate(e.target.checked)}
            />
            {t('appPage.security.hs256Label')}
          </label>
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            {t('appPage.security.hs256Help')}
          </p>
          <p className="text-sm" aria-live="polite">
            {hs256 ? t('appPage.security.hs256On') : t('appPage.security.hs256Off')}
          </p>
          <ErrorAlert error={toggle.error} />
        </section>

        <section className="space-y-2">
          <h3 className="text-sm font-semibold">{t('appPage.security.signOutTitle')}</h3>
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            {t('appPage.security.signOutHelp')}
          </p>
          <p className="text-sm" data-testid="member-sessions-valid-from">
            {app.member_sessions_valid_from
              ? t('appPage.security.signedOutAt', {
                  date: formatDateTime(app.member_sessions_valid_from),
                })
              : t('appPage.security.neverSignedOut')}
          </p>
          {editable && (
            <Button variant="danger" onClick={() => setConfirming(true)}>
              {t('appPage.security.signOutButton')}
            </Button>
          )}
        </section>
      </div>

      <Dialog
        open={confirming}
        onClose={close}
        title={t('appPage.security.signOutTitle')}
        footer={
          <>
            <Button variant="secondary" onClick={close} disabled={signOut.isPending}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              disabled={phrase !== SIGN_OUT_PHRASE || signOut.isPending}
              onClick={() => signOut.mutate(undefined)}
            >
              {t('appPage.security.signOutButton')}
            </Button>
          </>
        }
      >
        <Alert tone="warning">
          {t('appPage.security.signOutBody', { phrase: SIGN_OUT_PHRASE })}
        </Alert>
        <div className="mt-4">
          <Field
            id="sign-out-phrase"
            name="phrase"
            label={t('appPage.security.phraseLabel')}
            autoComplete="off"
            value={phrase}
            onChange={(e) => setPhrase(e.target.value)}
          />
        </div>
        <div className="mt-3">
          <ErrorAlert error={signOut.error} />
        </div>
      </Dialog>
    </Card>
  );
}
