import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { api, asApiError, unwrap } from '../../api/client';
import type { Session, TenantType } from '../../api/types';
import { ErrorAlert } from '../../components/feedback';
import { Button, Field, SelectField } from '../../components/ui';

export function RegisterForm({
  onRegistered,
}: {
  onRegistered: (session: Session) => Promise<void> | void;
}) {
  const { t } = useTranslation();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const displayName = String(fd.get('display_name') ?? '').trim();
    setPending(true);
    setError(null);
    void (async () => {
      try {
        const session = unwrap(
          await api.POST('/v1/dashboard/auth/register', {
            body: {
              email: String(fd.get('email') ?? ''),
              password: String(fd.get('password') ?? ''),
              ...(displayName ? { display_name: displayName } : {}),
              tenant_name: String(fd.get('tenant_name') ?? ''),
              tenant_type: String(fd.get('tenant_type') ?? 'brand') as TenantType,
            },
          }),
        );
        await onRegistered(session);
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
      <h1 className="text-xl font-semibold">{t('auth.register.title')}</h1>
      <Field
        id="reg-email"
        name="email"
        type="email"
        label={t('auth.login.email')}
        autoComplete="username"
        required
        error={apiError?.field('email')}
      />
      <Field
        id="reg-password"
        name="password"
        type="password"
        label={t('auth.login.password')}
        autoComplete="new-password"
        minLength={10}
        required
        hint={t('auth.register.passwordHint')}
        error={apiError?.field('password')}
      />
      <Field
        id="reg-display-name"
        name="display_name"
        label={`${t('auth.register.displayName')} (${t('common.optional')})`}
        autoComplete="name"
        error={apiError?.field('display_name')}
      />
      <Field
        id="reg-tenant-name"
        name="tenant_name"
        label={t('auth.register.tenantName')}
        required
        error={apiError?.field('tenant_name')}
      />
      <SelectField
        id="reg-tenant-type"
        name="tenant_type"
        label={t('auth.register.tenantType')}
        defaultValue="brand"
        error={apiError?.field('tenant_type')}
      >
        <option value="brand">{t('auth.register.brand')}</option>
        <option value="agency">{t('auth.register.agency')}</option>
      </SelectField>
      <ErrorAlert error={error} />
      <Button type="submit" className="w-full" disabled={pending}>
        {pending ? t('common.working') : t('auth.register.submit')}
      </Button>
    </form>
  );
}
