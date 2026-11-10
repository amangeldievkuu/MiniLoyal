import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

import type { ScannedMember } from '../api/types';
import { Badge, Card } from './ui';

/** Seconds until `expiresAt`, refreshed every second; never negative. */
export function useSecondsLeft(expiresAt: string): number {
  const target = Date.parse(expiresAt);
  const compute = () => Math.max(0, Math.ceil((target - Date.now()) / 1000));
  const [left, setLeft] = useState(compute);
  useEffect(() => {
    setLeft(compute());
    const id = setInterval(() => setLeft(compute()), 1000);
    return () => clearInterval(id);
    // compute depends only on target.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target]);
  return left;
}

function mmss(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}

/**
 * The scanned member: picture, name, friend status, balance and how long the
 * scan token stays valid. When it runs out, onExpire clears the member so
 * the cashier scans again.
 */
export function MemberPanel({
  member,
  balance,
  expiresAt,
  onExpire,
}: {
  member: ScannedMember;
  balance: number;
  expiresAt: string;
  onExpire: () => void;
}) {
  const { t } = useTranslation();
  const left = useSecondsLeft(expiresAt);

  useEffect(() => {
    if (left === 0) onExpire();
  }, [left, onExpire]);

  return (
    <Card className="flex items-center gap-4">
      {member.picture_url ? (
        <img
          src={member.picture_url}
          alt=""
          className="size-16 shrink-0 rounded-full object-cover"
        />
      ) : (
        <div aria-hidden className="size-16 shrink-0 rounded-full bg-zinc-200 dark:bg-zinc-800" />
      )}
      <div className="min-w-0 flex-1">
        <p className="truncate text-xl font-bold">{member.display_name ?? t('member.anonymous')}</p>
        <Badge tone={member.is_friend ? 'success' : 'neutral'}>
          {member.is_friend ? t('member.friend') : t('member.notFriend')}
        </Badge>
        <p className="mt-1 text-base">
          {t('member.balance')}:{' '}
          <strong className="font-mono text-2xl tabular-nums" data-testid="balance">
            {balance.toLocaleString()}
          </strong>
        </p>
        <p className="text-sm text-zinc-600 dark:text-zinc-400" aria-live="off">
          {t('member.expiresIn', { time: mmss(left) })}
        </p>
      </div>
    </Card>
  );
}
