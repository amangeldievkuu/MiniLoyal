import { useInfiniteQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';

import { securityEventsQuery } from '../api/queries';
import { EmptyState, ErrorAlert } from '../components/feedback';
import { Button, Card, Status, Table, Td, Th } from '../components/ui';
import { formatDateTime } from '../lib/format';

/** Human labels for the event names the API emits; unknown names show as is. */
export function eventLabel(t: TFunction, event: string): string {
  const labels: Record<string, string> = {
    'login.success': t('me.security.events.loginSuccess'),
    'login.failed': t('me.security.events.loginFailed'),
    'login.mfa_failed': t('me.security.events.mfaFailed'),
    'mfa.exhausted': t('me.security.events.mfaExhausted'),
    'password.changed': t('me.security.events.passwordChanged'),
    'totp.enabled': t('me.security.events.totpEnabled'),
    'totp.disabled': t('me.security.events.totpDisabled'),
    'sessions.ended': t('me.security.events.sessionsEnded'),
    'refresh.replayed': t('me.security.events.refreshReplayed'),
    'invitation.accepted': t('me.security.events.invitationAccepted'),
    'account.created': t('me.security.events.accountCreated'),
  };
  return labels[event] ?? event;
}

function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

/** The signed-in user's security history with "load more" paging. */
export function SecurityActivityCard() {
  const { t } = useTranslation();
  const page = useInfiniteQuery(securityEventsQuery);
  const events = page.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <Card title={t('me.security.title')} description={t('me.security.help')}>
      {page.isPending && <Status>{t('common.loading')}</Status>}
      {page.isError && <ErrorAlert error={page.error} />}
      {page.data && events.length === 0 && <EmptyState>{t('me.security.empty')}</EmptyState>}
      {events.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>{t('me.security.event')}</Th>
              <Th>{t('me.security.ip')}</Th>
              <Th>{t('me.security.userAgent')}</Th>
              <Th>{t('me.security.time')}</Th>
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {events.map((ev) => (
              <tr key={ev.id}>
                <Td className="font-medium">{eventLabel(t, ev.event)}</Td>
                <Td className="font-mono text-xs">{ev.ip ?? '—'}</Td>
                <Td className="text-zinc-600 dark:text-zinc-400">
                  <span title={ev.user_agent ?? undefined}>
                    {ev.user_agent ? truncate(ev.user_agent, 48) : '—'}
                  </span>
                </Td>
                <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                  {formatDateTime(ev.created_at)}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {page.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button
            variant="secondary"
            onClick={() => void page.fetchNextPage()}
            disabled={page.isFetchingNextPage}
          >
            {page.isFetchingNextPage ? t('common.working') : t('me.security.loadMore')}
          </Button>
        </div>
      )}
    </Card>
  );
}
