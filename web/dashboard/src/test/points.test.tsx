import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type {
  App,
  MemberDetail,
  PointsRules,
  PointsSummary,
  PointsTransaction,
  Staff,
} from '../api/types';
import { AppSecurityCard, SIGN_OUT_PHRASE } from '../pages/AppSecurityCard';
import { MemberDetailView } from '../pages/MemberDetailPage';
import { PointsRulesView } from '../pages/PointsRulesPage';
import { StaffList } from '../pages/StaffPage';
import { jsonResponse, renderWithProviders, stubFetch, type RecordedCall } from './providers';

afterEach(() => vi.unstubAllGlobals());

/** A button inside a native dialog (jsdom keeps dialogs out of the accessibility tree). */
const dialogButton = (name: string) => {
  const btn = screen.getAllByRole('button', { name, hidden: true }).at(-1);
  if (!btn) throw new Error(`button ${name} not rendered`);
  return btn;
};

const rules = (extra: Partial<PointsRules> = {}): PointsRules => ({
  currency: 'TWD',
  spend_per_point: 100,
  points_per_unit: 1,
  min_spend: 0,
  max_points_per_txn: null,
  expiry_days: 365,
  enabled: true,
  updated_at: '2026-11-01T00:00:00Z',
  ...extra,
});

describe('PointsRulesView', () => {
  it('previews the rule as it is edited and saves with an Idempotency-Key', async () => {
    const { calls } = stubFetch((call) => {
      if (call.path !== '/v1/dashboard/apps/a1/points-rules') return undefined;
      if (call.method === 'GET') return jsonResponse(200, rules());
      if (call.method === 'PUT') {
        return jsonResponse(200, { ...(call.body as object), updated_at: '2026-11-10T00:00:00Z' });
      }
      return undefined;
    });
    const user = userEvent.setup();
    renderWithProviders(<PointsRulesView appId="a1" role="owner" />);

    const preview = await screen.findByTestId('rules-preview');
    expect(preview).toHaveTextContent('Spend NT$100, earn 1 point; points expire after 365 days');

    await user.clear(screen.getByLabelText('Spend (NT$)'));
    await user.type(screen.getByLabelText('Spend (NT$)'), '50');
    await user.clear(screen.getByLabelText('Points earned'));
    await user.type(screen.getByLabelText('Points earned'), '2');
    await user.clear(screen.getByLabelText('Points expire after (days)'));
    await user.type(screen.getByLabelText('Most points per purchase'), '100');
    await user.clear(screen.getByLabelText('Minimum spend (NT$)'));
    await user.type(screen.getByLabelText('Minimum spend (NT$)'), '30');
    expect(preview).toHaveTextContent('Spend NT$50, earn 2 points; points never expire');
    expect(preview).toHaveTextContent('Purchases under NT$30 earn nothing.');
    expect(preview).toHaveTextContent('At most 100 points per purchase.');

    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    const put = calls.find((c) => c.method === 'PUT') as RecordedCall;
    expect(put.headers.get('Idempotency-Key')).toBeTruthy();
    expect(put.body).toEqual({
      currency: 'TWD',
      spend_per_point: 50,
      points_per_unit: 2,
      min_spend: 30,
      max_points_per_txn: 100,
      expiry_days: null,
      enabled: true,
    });
    expect(await screen.findByText('Saved.')).toBeInTheDocument();
  });

  it('turns the preview off when earning is disabled', async () => {
    stubFetch((call) =>
      call.path === '/v1/dashboard/apps/a1/points-rules'
        ? jsonResponse(200, rules({ enabled: false }))
        : undefined,
    );
    renderWithProviders(<PointsRulesView appId="a1" role="admin" />);
    expect(await screen.findByTestId('rules-preview')).toHaveTextContent(
      'Earning points is turned off.',
    );
  });

  it('is read only for the staff role', async () => {
    stubFetch((call) =>
      call.path === '/v1/dashboard/apps/a1/points-rules' ? jsonResponse(200, rules()) : undefined,
    );
    renderWithProviders(<PointsRulesView appId="a1" role="staff" />);
    expect(await screen.findByLabelText('Spend (NT$)')).toBeDisabled();
    expect(screen.getByText('Your role can only view this page.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
  });
});

function member(extra: Partial<MemberDetail> = {}): MemberDetail {
  return {
    id: 'm1',
    app_id: 'a1',
    display_name: 'Mei',
    picture_url: null,
    language: 'zh-TW',
    is_friend: true,
    custom_fields: {},
    line_user_id: 'Um1',
    phone: '0912345678',
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-02T00:00:00Z',
    ...extra,
  };
}

const summary = (balance: number): PointsSummary => ({
  member_id: 'm1',
  balance,
  lifetime_earned: 900,
  expiring: [{ points: 40, expires_at: '2026-12-01T00:00:00Z' }],
  rules: { currency: 'TWD', spend_per_point: 100, points_per_unit: 1, enabled: true },
});

const tx = (id: string, extra: Partial<PointsTransaction> = {}): PointsTransaction => ({
  id,
  member_id: 'm1',
  kind: 'earn',
  points: 12,
  balance_after: 120,
  spend_amount: 1200,
  ref_type: 'receipt',
  ref_id: `R-${id}`,
  note: null,
  store_id: 's1',
  actor_type: 'staff',
  created_at: '2026-11-05T00:00:00Z',
  ...extra,
});

/** Routes every GET the member detail page makes. */
function memberRoutes(opts: {
  member?: MemberDetail;
  balance?: number;
  onPost?: (call: RecordedCall) => Response | undefined;
}) {
  return stubFetch((call) => {
    if (call.method === 'POST') return opts.onPost?.(call);
    switch (call.path) {
      case '/v1/dashboard/members/m1':
        return jsonResponse(200, opts.member ?? member());
      case '/v1/dashboard/members/m1/points':
        return jsonResponse(200, summary(opts.balance ?? 120));
      case '/v1/dashboard/members/m1/points/history':
        if (call.query.get('cursor') === 'p2') {
          return jsonResponse(200, {
            items: [tx('t3', { kind: 'expire', points: -5, actor_type: 'system', store_id: null })],
            next_cursor: null,
          });
        }
        return jsonResponse(200, {
          items: [
            tx('t1'),
            tx('t2', { kind: 'redeem', points: -30, spend_amount: null, note: 'Coffee' }),
          ],
          next_cursor: 'p2',
        });
      case '/v1/dashboard/apps/a1/stores':
        return jsonResponse(200, [
          {
            id: 's1',
            app_id: 'a1',
            name: 'Zhongxiao',
            status: 'active',
            created_at: '2026-11-01T00:00:00Z',
          },
        ]);
      default:
        return undefined;
    }
  });
}

describe('Member points', () => {
  it('shows the summary and loads more history', async () => {
    memberRoutes({});
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="staff" />);

    expect(await screen.findByTestId('points-balance')).toHaveTextContent('120');
    expect(screen.getByText('900')).toBeInTheDocument();
    expect(screen.getByText(/^40 on /)).toBeInTheDocument();
    expect(await screen.findByText('+12')).toBeInTheDocument();
    expect(screen.getByText('-30')).toBeInTheDocument();
    expect(screen.getByText('Coffee')).toBeInTheDocument();
    expect(screen.getByText('receipt: R-t1')).toBeInTheDocument();
    expect(await screen.findAllByText('Zhongxiao')).not.toHaveLength(0);
    expect(screen.getByText('NT$1,200')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('Expired')).toBeInTheDocument();
    expect(screen.getByText('-5')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
    // Staff can look but not adjust.
    expect(screen.queryByRole('button', { name: 'Adjust points' })).not.toBeInTheDocument();
  });

  it('sends a signed delta and shows a 409 inline', async () => {
    const { calls } = memberRoutes({
      balance: 10,
      onPost: (call) =>
        call.path === '/v1/dashboard/members/m1/points/adjustments'
          ? jsonResponse(409, {
              code: 'insufficient_points',
              message: 'not enough points',
              request_id: 'r1',
            })
          : undefined,
    });
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="admin" />);

    await user.click(await screen.findByRole('button', { name: 'Adjust points' }));
    await user.type(screen.getByLabelText('Change'), '-8');
    expect(screen.getByTestId('adjust-resulting')).toHaveTextContent('Balance after: 2');
    await user.type(screen.getByLabelText('Reason'), 'Wrong receipt');
    await user.click(dialogButton('Apply'));

    await waitFor(() =>
      expect(
        screen.getByText('The member does not have enough points for this change.'),
      ).toBeInTheDocument(),
    );
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.body).toEqual({ delta: -8, note: 'Wrong receipt' });
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy();
  });

  it('warns about a negative result and refuses zero', async () => {
    memberRoutes({ balance: 5 });
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="owner" />);

    await user.click(await screen.findByRole('button', { name: 'Adjust points' }));
    await user.type(screen.getByLabelText('Change'), '0');
    expect(screen.getByText('Enter a whole number other than zero.')).toBeInTheDocument();
    expect(dialogButton('Apply')).toBeDisabled();
    await user.clear(screen.getByLabelText('Change'));
    await user.type(screen.getByLabelText('Change'), '-9');
    expect(screen.getByText('This would take the balance below zero.')).toBeInTheDocument();
  });

  it('resets the member card only after confirmation', async () => {
    const { calls } = memberRoutes({
      onPost: (call) =>
        call.path === '/v1/dashboard/members/m1/card/rotate'
          ? jsonResponse(200, { qr_key_version: 3 })
          : undefined,
    });
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="owner" />);

    await user.click(await screen.findByRole('button', { name: 'Reset member card' }));
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    expect(
      screen.getByText(
        'Every QR code this member’s card showed stops working at once. Continue?'.replace(
          '’',
          "'",
        ),
      ),
    ).toBeInTheDocument();
    await user.click(dialogButton('Reset member card'));

    expect(
      await screen.findByText('Card reset. Old QR codes no longer work (key version 3).'),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.path === '/v1/dashboard/members/m1/card/rotate');
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy();
  });

  it('shows the lock message for fields hidden from the staff role', async () => {
    memberRoutes({
      member: member({ line_user_id: undefined, phone: undefined, birthday: undefined }),
    });
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="staff" />);

    expect(await screen.findByText('Mei')).toBeInTheDocument();
    const locks = screen.getAllByTestId('restricted');
    // LINE user ID, phone, birthday and the custom fields card.
    expect(locks).toHaveLength(4);
    expect(locks[0]).toHaveTextContent('Visible to owners and admins');
    expect(screen.queryByRole('button', { name: 'Reset member card' })).not.toBeInTheDocument();
  });

  it('shows a dash, not the lock, when an owner sees an empty field', async () => {
    memberRoutes({ member: member({ phone: null }) });
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="owner" />);
    expect(await screen.findByText('Um1')).toBeInTheDocument();
    expect(screen.queryByTestId('restricted')).not.toBeInTheDocument();
  });
});

const app = (extra: Partial<App> = {}): App => ({
  id: 'a1',
  tenant_id: 't1',
  name: 'Cafe',
  slug: 'cafe',
  locale: 'zh-TW',
  status: 'active',
  oa_channel_secret_set: false,
  webhook_url: 'https://api.example.com/webhooks/line/a1',
  allow_hs256_id_tokens: false,
  member_sessions_valid_from: null,
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
  ...extra,
});

describe('AppSecurityCard', () => {
  it('signs every member out only after the phrase is typed', async () => {
    const { calls } = stubFetch((call) =>
      call.method === 'POST' && call.path === '/v1/dashboard/apps/a1/member-sessions/end'
        ? jsonResponse(200, app({ member_sessions_valid_from: '2026-11-10T03:00:00Z' }))
        : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<AppSecurityCard app={app()} editable />);

    expect(
      await screen.findByText('Members have never been signed out all at once.'),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Sign out all members' }));
    const confirm = dialogButton('Sign out all members');
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText('Confirmation'), 'SIGN');
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText('Confirmation'), SIGN_OUT_PHRASE.slice(4));
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    expect(calls[0]?.headers.get('Idempotency-Key')).toBeTruthy();
  });

  it('toggles HS256 with a PATCH and stays read only for staff', async () => {
    const { calls } = stubFetch((call) =>
      call.method === 'PATCH' && call.path === '/v1/dashboard/apps/a1'
        ? jsonResponse(200, app({ allow_hs256_id_tokens: true }))
        : undefined,
    );
    const user = userEvent.setup();
    const { unmount } = renderWithProviders(<AppSecurityCard app={app()} editable />);
    expect(await screen.findByText('Only ES256 tokens are accepted.')).toBeInTheDocument();
    await user.click(screen.getByLabelText('Accept HS256 ID tokens'));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body).toEqual({ allow_hs256_id_tokens: true });
    expect(calls[0]?.headers.get('Idempotency-Key')).toBeTruthy();
    unmount();

    renderWithProviders(
      <AppSecurityCard
        app={app({ member_sessions_valid_from: '2026-11-10T03:00:00Z' })}
        editable={false}
      />,
    );
    expect(await screen.findByLabelText('Accept HS256 ID tokens')).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Sign out all members' })).not.toBeInTheDocument();
    expect(screen.getByTestId('member-sessions-valid-from')).toHaveTextContent(
      'Last signed out everyone:',
    );
  });
});

const staff = (extra: Partial<Staff> = {}): Staff => ({
  id: 'st1',
  app_id: 'a1',
  store_id: null,
  display_name: 'Kenji',
  role: 'cashier',
  status: 'active',
  line_linked: true,
  last_login_at: '2026-11-09T08:00:00Z',
  created_at: '2026-11-01T00:00:00Z',
  ...extra,
});

describe('Staff invite reset', () => {
  it('confirms, then shows the new code once', async () => {
    const { calls } = stubFetch((call) => {
      if (call.method === 'GET' && call.path === '/v1/dashboard/apps/a1/staff') {
        return jsonResponse(200, [
          staff(),
          staff({ id: 'st2', display_name: 'Ana', line_linked: false, last_login_at: null }),
        ]);
      }
      if (call.method === 'GET' && call.path === '/v1/dashboard/apps/a1/stores') {
        return jsonResponse(200, []);
      }
      if (call.method === 'POST' && call.path === '/v1/dashboard/staff/st1/invite') {
        return jsonResponse(200, {
          ...staff({ line_linked: false }),
          invite_code: 'ABCD2345EFGH',
        });
      }
      return undefined;
    });
    const user = userEvent.setup();
    renderWithProviders(<StaffList appId="a1" role="admin" />);

    const row = (await screen.findByText('Kenji')).closest('tr');
    if (!row) throw new Error('row not rendered');
    expect(within(row).getByText('Linked')).toBeInTheDocument();
    expect(screen.getByText('Never')).toBeInTheDocument();

    await user.click(within(row).getByRole('button', { name: 'New invite code' }));
    expect(
      screen.getByText(
        '"Kenji" is signed out of the staff app and their LINE account is unlinked. They sign in again with the new code.',
      ),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    await user.click(dialogButton('New invite code'));

    expect(await screen.findByText('ABCD2345EFGH')).toBeInTheDocument();
    expect(screen.getByText(/Give this 12-character code/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy();

    await user.click(dialogButton('Done'));
    await waitFor(() => expect(screen.queryByText('ABCD2345EFGH')).not.toBeInTheDocument());
  });

  it('hides the action from the staff role', async () => {
    stubFetch((call) => {
      if (call.path === '/v1/dashboard/apps/a1/staff') return jsonResponse(200, [staff()]);
      if (call.path === '/v1/dashboard/apps/a1/stores') return jsonResponse(200, []);
      return undefined;
    });
    renderWithProviders(<StaffList appId="a1" role="staff" />);
    expect(await screen.findByText('Kenji')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New invite code' })).not.toBeInTheDocument();
  });
});
