import { useInfiniteQuery } from '@tanstack/react-query';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

import { membersQuery } from '../api/queries';
import type { MemberDetail } from '../api/types';
import { AppHeader } from '../components/AppHeader';
import { EmptyState, ErrorAlert, PageHeader } from '../components/feedback';
import { Badge, Button, Card, Input, Status, Table, Td, Th } from '../components/ui';
import { formatDateTime } from '../lib/format';

export function MembersPage() {
  const { appId = '' } = useParams({ strict: false });
  return <AppHeader appId={appId}>{() => <MembersList appId={appId} />}</AppHeader>;
}

/** A member's picture, or a neutral placeholder when LINE shared none. */
export function Avatar({ member }: { member: Pick<MemberDetail, 'picture_url' | 'display_name'> }) {
  if (member.picture_url) {
    return (
      <img
        src={member.picture_url}
        alt=""
        width={32}
        height={32}
        className="h-8 w-8 rounded-full bg-zinc-100 object-cover dark:bg-zinc-800"
      />
    );
  }
  return (
    <span
      aria-hidden="true"
      className="inline-flex h-8 w-8 items-center justify-center rounded-full bg-zinc-200 text-xs font-semibold text-zinc-600 dark:bg-zinc-700 dark:text-zinc-200"
    >
      {(member.display_name ?? '?').slice(0, 1).toUpperCase()}
    </span>
  );
}

export function FriendBadge({ isFriend }: { isFriend: boolean }) {
  const { t } = useTranslation();
  return (
    <Badge tone={isFriend ? 'success' : 'neutral'}>
      {isFriend ? t('members.isFriend') : t('members.notFriend')}
    </Badge>
  );
}

/** Searchable, "load more" list of an app's members. */
export function MembersList({ appId }: { appId: string }) {
  const { t } = useTranslation();
  const [q, setQ] = useState<string | undefined>(undefined);
  const page = useInfiniteQuery(membersQuery(appId, q));
  const members = page.data?.pages.flatMap((p) => p.items) ?? [];

  function submitSearch(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const text = String(new FormData(e.currentTarget).get('q') ?? '').trim();
    setQ(text || undefined);
  }

  return (
    <div>
      <PageHeader title={t('members.title')} description={t('members.help')} />
      <Card
        actions={
          <form onSubmit={submitSearch} role="search" className="flex flex-wrap gap-2">
            <Input
              name="q"
              type="search"
              aria-label={t('members.search')}
              placeholder={t('members.searchPlaceholder')}
              defaultValue={q ?? ''}
              maxLength={100}
              className="w-auto min-w-56"
            />
            <Button type="submit" variant="secondary">
              {t('members.search')}
            </Button>
          </form>
        }
      >
        {page.isPending && <Status>{t('common.loading')}</Status>}
        {page.isError && <ErrorAlert error={page.error} />}
        {page.data && members.length === 0 && (
          <EmptyState>{q ? t('members.noResults') : t('members.empty')}</EmptyState>
        )}
        {members.length > 0 && (
          <Table>
            <thead>
              <tr>
                <Th>{t('members.member')}</Th>
                <Th>{t('members.friend')}</Th>
                <Th>{t('members.consent')}</Th>
                <Th>{t('members.joined')}</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {members.map((member) => (
                <tr key={member.id}>
                  <Td>
                    <Link
                      to="/apps/$appId/members/$memberId"
                      params={{ appId, memberId: member.id }}
                      className="flex items-center gap-3 font-medium hover:underline"
                    >
                      <Avatar member={member} />
                      <span>{member.display_name ?? t('members.anonymous')}</span>
                    </Link>
                  </Td>
                  <Td>
                    <FriendBadge isFriend={member.is_friend} />
                  </Td>
                  <Td className="text-zinc-600 dark:text-zinc-400">
                    {member.consent ? member.consent.version : t('members.noConsent')}
                  </Td>
                  <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                    {formatDateTime(member.created_at)}
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
              {page.isFetchingNextPage ? t('common.working') : t('members.loadMore')}
            </Button>
          </div>
        )}
      </Card>
    </div>
  );
}
