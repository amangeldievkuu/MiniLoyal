import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';

import { Button, Card } from '../components/ui';

export function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <div className="mx-auto max-w-md px-4 py-16">
      <Card title={t('nav.notFound')} description={t('nav.notFoundHelp')}>
        <Link to="/">
          <Button variant="secondary">{t('nav.goHome')}</Button>
        </Link>
      </Card>
    </div>
  );
}

/** Shown at / when the signed-in user belongs to no tenant. */
export function NoTenantPage() {
  const { t } = useTranslation();
  return (
    <Card title={t('nav.noTenant')} description={t('nav.noTenantHelp')}>
      <Link to="/register">
        <Button variant="secondary">{t('auth.login.register')}</Button>
      </Link>
    </Card>
  );
}
