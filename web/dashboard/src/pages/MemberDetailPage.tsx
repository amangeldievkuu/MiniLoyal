import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { api, unwrap } from '../api/client';
import { keys, memberQuery } from '../api/queries';
import type { MemberDetail, Role } from '../api/types';
import { AppHeader } from '../components/AppHeader';
import { CopyButton, ErrorAlert } from '../components/feedback';
import { Alert, Badge, Button, Card, Dialog, Field, Status } from '../components/ui';
import { downloadJson } from '../lib/download';
import { formatDateTime } from '../lib/format';
import { canManage } from '../lib/roles';
import { MemberCouponsPanel } from './MemberCoupons';
import { MemberCardCard, MemberPointsPanel } from './MemberPoints';
import { MemberStampsPanel } from './MemberStamps';
import { Avatar, FriendBadge } from './MembersPage';

/** The word the operator types to confirm an erasure; the same in every language. */
export const ERASE_PHRASE = 'ERASE';

const MEMBER_TABS = ['overview', 'coupons', 'stamps'] as const;
type MemberTab = (typeof MEMBER_TABS)[number];

export function MemberDetailPage() {
  const { appId = '', memberId = '' } = useParams({ strict: false });
  return (
    <AppHeader appId={appId}>
      {(_app, role) => <MemberDetailView appId={appId} memberId={memberId} role={role} />}
    </AppHeader>
  );
}

/** Shown instead of a field the staff role may not see. */
export function Restricted() {
  const { t } = useTranslation();
  return (
    <span className="inline-flex items-center gap-1 text-zinc-500" data-testid="restricted">
      <svg aria-hidden="true" viewBox="0 0 20 20" className="h-3.5 w-3.5 fill-current">
        <path d="M10 2a4 4 0 0 0-4 4v2H5a1 1 0 0 0-1 1v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V9a1 1 0 0 0-1-1h-1V6a4 4 0 0 0-4-4Zm-2 6V6a2 2 0 1 1 4 0v2H8Z" />
      </svg>
      {t('members.detail.restricted')}
    </span>
  );
}

function Row({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <>
      <dt className="text-zinc-500">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  );
}

export function MemberDetailView({
  appId,
  memberId,
  role,
}: {
  appId: string;
  memberId: string;
  role: Role | null;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const member = useQuery(memberQuery(memberId));
  const [tab, setTab] = useState<MemberTab>('overview');
  const [erasing, setErasing] = useState(false);
  const [phrase, setPhrase] = useState('');
  const editable = canManage(role);
  const dash = t('members.detail.notAvailable');
  // The API omits personal fields for the staff role; say so instead of a dash.
  const missing = role === 'staff' ? <Restricted /> : dash;

  const exportData = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.GET('/v1/dashboard/members/{memberId}/export', {
          params: { path: { memberId } },
        }),
      ),
    onSuccess: (data) => downloadJson(`member-${memberId}.json`, data),
  });

  const erase = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.DELETE('/v1/dashboard/members/{memberId}', { params: { path: { memberId } } }),
      ),
    onSuccess: () => {
      setErasing(false);
      void qc.invalidateQueries({ queryKey: keys.membersRoot(appId) });
      qc.removeQueries({ queryKey: keys.member(memberId) });
      void navigate({ to: '/apps/$appId/members', params: { appId } });
    },
  });

  if (member.isPending) return <Status>{t('common.loading')}</Status>;
  if (member.isError) return <ErrorAlert error={member.error} />;
  const m: MemberDetail = member.data;
  const customFields = Object.entries(m.custom_fields);

  return (
    <div className="space-y-6">
      <Link
        to="/apps/$appId/members"
        params={{ appId }}
        className="text-sm text-zinc-600 hover:underline dark:text-zinc-400"
      >
        ← {t('members.detail.back')}
      </Link>

      <Card
        title={
          <span className="flex items-center gap-3">
            <Avatar member={m} />
            {m.display_name ?? t('members.anonymous')}
          </span>
        }
        actions={<FriendBadge isFriend={m.is_friend} />}
      >
        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
          <Row label={t('members.detail.lineUserId')}>
            {m.line_user_id ? (
              <span className="flex flex-wrap items-center gap-2">
                <code className="font-mono text-xs break-all">{m.line_user_id}</code>
                <CopyButton text={m.line_user_id} />
              </span>
            ) : (
              missing
            )}
          </Row>
          <Row label={t('members.detail.language')}>{m.language ?? dash}</Row>
          <Row label={t('members.detail.phone')}>{m.phone ?? missing}</Row>
          <Row label={t('members.detail.birthday')}>{m.birthday ?? missing}</Row>
          <Row label={t('members.detail.gender')}>
            {m.gender ? t(`members.detail.genders.${m.gender}`) : dash}
          </Row>
          <Row label={t('members.detail.friendChangedAt')}>
            {m.friend_changed_at ? formatDateTime(m.friend_changed_at) : dash}
          </Row>
          <Row label={t('members.consent')}>
            {m.consent ? (
              <Badge tone="success">
                {t('members.detail.consentAccepted', {
                  version: m.consent.version,
                  date: formatDateTime(m.consent.accepted_at),
                })}
              </Badge>
            ) : (
              <Badge tone="warning">{t('members.noConsent')}</Badge>
            )}
          </Row>
          <Row label={t('members.detail.lastLogin')}>
            {m.last_login_at ? formatDateTime(m.last_login_at) : dash}
          </Row>
          <Row label={t('members.joined')}>{formatDateTime(m.created_at)}</Row>
          <Row label={t('members.detail.updatedAt')}>{formatDateTime(m.updated_at)}</Row>
        </dl>
      </Card>

      <Card title={t('members.detail.customFields')}>
        {customFields.length === 0 ? (
          <p className="text-sm text-zinc-500">{missing}</p>
        ) : (
          <pre className="overflow-x-auto rounded-md bg-zinc-100 p-3 text-xs dark:bg-zinc-800">
            {JSON.stringify(m.custom_fields, null, 2)}
          </pre>
        )}
      </Card>

      <div role="tablist" aria-label={t('members.tabs.label')} className="flex gap-1 border-b pb-2">
        {MEMBER_TABS.map((key) => (
          <button
            key={key}
            type="button"
            role="tab"
            id={`member-tab-${key}`}
            aria-selected={tab === key}
            aria-controls="member-tabpanel"
            onClick={() => setTab(key)}
            className="rounded-md px-3 py-2 text-sm font-medium aria-selected:bg-brand-50 aria-selected:text-brand-700 hover:bg-zinc-100 dark:hover:bg-zinc-800 dark:aria-selected:bg-zinc-800"
          >
            {t(`members.tabs.${key}`)}
          </button>
        ))}
      </div>

      <div
        role="tabpanel"
        id="member-tabpanel"
        aria-labelledby={`member-tab-${tab}`}
        className="space-y-6"
      >
        {tab === 'coupons' && (
          <MemberCouponsPanel appId={appId} memberId={memberId} editable={editable} />
        )}
        {tab === 'stamps' && <MemberStampsPanel memberId={memberId} editable={editable} />}
      </div>

      {tab === 'overview' && (
        <MemberPointsPanel appId={appId} memberId={memberId} editable={editable} />
      )}

      {tab === 'overview' && editable && <MemberCardCard memberId={memberId} />}

      {tab === 'overview' && editable && (
        <Card
          title={t('members.detail.privacyTitle')}
          description={t('members.detail.privacyHelp')}
        >
          <div className="flex flex-wrap gap-2">
            <Button
              variant="secondary"
              onClick={() => exportData.mutate()}
              disabled={exportData.isPending}
            >
              {exportData.isPending ? t('common.working') : t('members.detail.export')}
            </Button>
            <Button variant="danger" onClick={() => setErasing(true)}>
              {t('members.detail.erase')}
            </Button>
          </div>
          <div className="mt-3">
            <ErrorAlert error={exportData.error} />
          </div>
        </Card>
      )}

      <Dialog
        open={erasing}
        onClose={() => {
          setErasing(false);
          setPhrase('');
          erase.reset();
        }}
        title={t('members.detail.eraseTitle')}
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setErasing(false);
                setPhrase('');
              }}
              disabled={erase.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              disabled={phrase !== ERASE_PHRASE || erase.isPending}
              onClick={() => erase.mutate()}
            >
              {t('members.detail.erase')}
            </Button>
          </>
        }
      >
        <Alert tone="warning">{t('members.detail.eraseBody', { phrase: ERASE_PHRASE })}</Alert>
        <div className="mt-4">
          <Field
            id="erase-phrase"
            name="phrase"
            label={t('members.detail.erasePhraseLabel')}
            autoComplete="off"
            value={phrase}
            onChange={(e) => setPhrase(e.target.value)}
          />
        </div>
        <div className="mt-3">
          <ErrorAlert error={erase.error} />
        </div>
      </Dialog>
    </div>
  );
}
