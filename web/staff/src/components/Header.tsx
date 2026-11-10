import { useTranslation } from 'react-i18next';

import type { StaffSession } from '../api/types';
import { signOut } from '../auth/session';
import { LANGUAGES, setLanguage, type Language } from '../i18n';
import { Badge, Button } from './ui';

export function Header({ session }: { session: StaffSession }) {
  const { t, i18n } = useTranslation();
  const { staff, app } = session;
  return (
    <header className="sticky top-0 z-10 border-b border-zinc-200 bg-white/95 px-4 py-3 backdrop-blur dark:border-zinc-800 dark:bg-zinc-950/95">
      <div className="mx-auto flex max-w-md items-center gap-3">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm text-zinc-600 dark:text-zinc-400">{app.name}</p>
          <p className="flex flex-wrap items-center gap-2">
            <span className="truncate text-lg font-semibold">{staff.display_name}</span>
            <Badge tone={staff.role === 'manager' ? 'success' : 'neutral'}>
              {staff.role === 'manager' ? t('header.manager') : t('header.cashier')}
            </Badge>
          </p>
          <p className="truncate text-sm text-zinc-600 dark:text-zinc-400">
            {staff.store?.name ?? t('header.noStore')}
          </p>
        </div>
        <label className="sr-only" htmlFor="lang">
          {t('app.language')}
        </label>
        <select
          id="lang"
          value={i18n.language}
          onChange={(e) => setLanguage(e.target.value as Language)}
          className="min-h-12 rounded-xl border border-zinc-300 bg-white px-2 dark:border-zinc-700 dark:bg-zinc-900"
        >
          {LANGUAGES.map((l) => (
            <option key={l.code} value={l.code}>
              {l.label}
            </option>
          ))}
        </select>
        <Button variant="secondary" onClick={signOut}>
          {t('common.signOut')}
        </Button>
      </div>
    </header>
  );
}
