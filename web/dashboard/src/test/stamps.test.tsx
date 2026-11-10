import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { MemberDetail, StampCard, StampProgress } from '../api/types';
import { toStampCreate, toStampUpdate, validateStamp } from '../features/stamps/stampForm';
import { MemberDetailView } from '../pages/MemberDetailPage';
import { StampCardsView } from '../pages/StampCardsPage';
import { jsonResponse, renderWithProviders, stubFetch, type RecordedCall } from './providers';

afterEach(() => vi.unstubAllGlobals());

const dialogButton = (name: string) => {
  const btn = screen.getAllByRole('button', { name, hidden: true }).at(-1);
  if (!btn) throw new Error(`button ${name} not rendered`);
  return btn;
};

const card = (extra: Partial<StampCard> = {}): StampCard => ({
  id: 's1',
  app_id: 'a1',
  name: 'Coffee card',
  stamps_required: 10,
  reward_coupon_template_id: 'c1',
  max_stamps_per_visit: 1,
  status: 'active',
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
  ...extra,
});

const template = (id: string, name: string, status: 'active' | 'archived' = 'active') => ({
  id,
  app_id: 'a1',
  name,
  description: null,
  type: 'free_item',
  value: null,
  total_limit: null,
  per_member_limit: 1,
  valid_from: null,
  valid_to: null,
  validity_days: null,
  claimable: false,
  claimed_count: 0,
  status,
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
});

const apiError = (status: number, code: string) =>
  jsonResponse(status, { code, message: `msg ${code}`, request_id: 'r1' });

describe('stamp form logic', () => {
  it('validates ranges', () => {
    const ok = {
      name: 'x',
      stamps_required: '10',
      reward_coupon_template_id: '',
      max_stamps_per_visit: '1',
    };
    expect(validateStamp(ok)).toEqual({});
    expect(validateStamp({ ...ok, stamps_required: '1' }).stamps_required).toBe('stampsRequired');
    expect(validateStamp({ ...ok, stamps_required: '101' }).stamps_required).toBe('stampsRequired');
    expect(validateStamp({ ...ok, max_stamps_per_visit: '0' }).max_stamps_per_visit).toBe(
      'maxPerVisit',
    );
    expect(toStampCreate(ok)).toEqual({ name: 'x', stamps_required: 10, max_stamps_per_visit: 1 });
  });

  it('clears the reward when the picker is emptied', () => {
    const c = card();
    expect(
      toStampUpdate(
        {
          name: 'Coffee card',
          stamps_required: '10',
          reward_coupon_template_id: '',
          max_stamps_per_visit: '1',
        },
        c,
      ),
    ).toEqual({ clear: ['reward_coupon_template_id'] });
  });
});

describe('StampCardsView', () => {
  const routes = (list: StampCard[], onWrite?: (c: RecordedCall) => Response | undefined) =>
    stubFetch((call) => {
      if (call.method === 'GET' && call.path === '/v1/dashboard/apps/a1/stamp-cards') {
        return jsonResponse(200, list);
      }
      if (call.method === 'GET' && call.path === '/v1/dashboard/apps/a1/coupon-templates') {
        return jsonResponse(200, [
          template('c1', 'Free coffee'),
          template('c2', 'Old', 'archived'),
        ]);
      }
      return onWrite?.(call);
    });

  it('lists cards with the reward name', async () => {
    routes([card()]);
    renderWithProviders(<StampCardsView appId="a1" role="owner" />);
    expect(await screen.findByText('Coffee card')).toBeInTheDocument();
    expect(await screen.findByText('Free coffee')).toBeInTheDocument();
  });

  it('is read only for staff', async () => {
    routes([card()]);
    renderWithProviders(<StampCardsView appId="a1" role="staff" />);
    expect(await screen.findByText('Coffee card')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New stamp card' })).not.toBeInTheDocument();
    expect(screen.getByText('Your role can only view this page.')).toBeInTheDocument();
  });

  it('validates, offers only active templates, and creates with a key', async () => {
    const { calls } = routes([], (c) =>
      c.method === 'POST' ? jsonResponse(201, card()) : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<StampCardsView appId="a1" role="admin" />);

    await user.click(await screen.findByRole('button', { name: 'New stamp card' }));
    await user.type(screen.getByLabelText('Name'), 'Tea card');
    await user.clear(screen.getByLabelText('Stamps to complete'));
    await user.type(screen.getByLabelText('Stamps to complete'), '1');
    await user.click(dialogButton('Save'));
    expect(await screen.findByText('Enter a whole number from 2 to 100.')).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);

    await screen.findByRole('option', { name: 'Free coffee', hidden: true });
    expect(screen.queryByRole('option', { name: 'Old', hidden: true })).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Reward coupon'), 'c1');
    await user.clear(screen.getByLabelText('Stamps to complete'));
    await user.type(screen.getByLabelText('Stamps to complete'), '8');
    await user.click(dialogButton('Save'));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST') as RecordedCall;
    expect(post.body).toEqual({
      name: 'Tea card',
      stamps_required: 8,
      max_stamps_per_visit: 1,
      reward_coupon_template_id: 'c1',
    });
    expect(post.headers.get('Idempotency-Key')).toBeTruthy();
  });

  it('disables stamps_required with an explanation after 409 stamp_card_in_use', async () => {
    const { calls } = routes([card()], (c) =>
      c.method === 'PATCH' ? apiError(409, 'stamp_card_in_use') : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<StampCardsView appId="a1" role="owner" />);

    await user.click(await screen.findByRole('button', { name: 'Edit: Coffee card' }));
    await user.clear(screen.getByLabelText('Stamps to complete'));
    await user.type(screen.getByLabelText('Stamps to complete'), '12');
    await user.click(dialogButton('Save'));

    await waitFor(() => expect(screen.getByLabelText('Stamps to complete')).toBeDisabled());
    expect(screen.getByText(/cannot change/, { selector: 'div,p' })).toBeInTheDocument();
    expect(
      screen.getByText('Locked: members already have stamps on this card.'),
    ).toBeInTheDocument();
    expect(screen.getByLabelText('Stamps to complete')).toHaveValue('10');
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1);
  });

  it('archives with a PATCH status', async () => {
    const { calls } = routes([card()], (c) =>
      c.method === 'PATCH' ? jsonResponse(200, card({ status: 'archived' })) : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<StampCardsView appId="a1" role="owner" />);
    await user.click(await screen.findByRole('button', { name: 'Archive: Coffee card' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH') as RecordedCall;
    expect(patch.body).toEqual({ status: 'archived' });
    expect(patch.headers.get('Idempotency-Key')).toBeTruthy();
  });
});

const progress = (extra: Partial<StampProgress> = {}): StampProgress => ({
  stamp_card_id: 's1',
  name: 'Coffee card',
  stamps_required: 10,
  max_stamps_per_visit: 1,
  status: 'active',
  count: 4,
  completions: 2,
  reward: { id: 'c1', name: 'Free coffee', type: 'free_item', value: null },
  unrewarded_completions: 1,
  updated_at: '2026-11-05T00:00:00Z',
  ...extra,
});

const member: MemberDetail = {
  id: 'm1',
  app_id: 'a1',
  display_name: 'Mei',
  picture_url: null,
  language: 'zh-TW',
  is_friend: true,
  custom_fields: {},
  line_user_id: 'Um1',
  phone: null,
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-02T00:00:00Z',
};

describe('Member stamps tab', () => {
  const routes = (onPost?: (c: RecordedCall) => Response | undefined) =>
    stubFetch((call) => {
      if (call.method === 'POST') return onPost?.(call);
      switch (call.path) {
        case '/v1/dashboard/members/m1':
          return jsonResponse(200, member);
        case '/v1/dashboard/members/m1/stamps':
          return jsonResponse(200, [progress()]);
        case '/v1/dashboard/members/m1/coupons':
          return jsonResponse(200, { items: [], next_cursor: null });
        default:
          return undefined;
      }
    });

  it('shows progress and the unrewarded hint, and lets staff look only', async () => {
    routes();
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="staff" />);

    await user.click(await screen.findByRole('tab', { name: 'Stamps' }));
    expect(await screen.findByTestId('stamp-progress-s1')).toHaveTextContent('4 of 10 stamps');
    expect(screen.getByText('1 completed without a reward coupon.')).toBeInTheDocument();
    expect(screen.getByText(/issue a coupon by hand/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Adjust stamps/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Coupons' }));
    expect(await screen.findByText('This member has no coupons.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Issue coupon' })).not.toBeInTheDocument();
  });

  it('adjusts with a note and maps insufficient_stamps', async () => {
    const { calls } = routes((c) =>
      c.path === '/v1/dashboard/members/m1/stamps/adjustments'
        ? apiError(409, 'insufficient_stamps')
        : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="owner" />);

    await user.click(await screen.findByRole('tab', { name: 'Stamps' }));
    await user.click(await screen.findByRole('button', { name: 'Adjust stamps: Coffee card' }));
    await user.type(screen.getByLabelText('Change'), '0');
    expect(
      screen.getByText('Enter a whole number other than zero, up to 100.'),
    ).toBeInTheDocument();
    await user.clear(screen.getByLabelText('Change'));
    await user.type(screen.getByLabelText('Change'), '-9');
    await user.type(screen.getByLabelText('Reason'), 'Wrong scan');
    await user.click(dialogButton('Apply'));

    expect(
      await screen.findByText('The member does not have that many stamps to remove.'),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.method === 'POST') as RecordedCall;
    expect(post.body).toEqual({ stamp_card_id: 's1', delta: -9, note: 'Wrong scan' });
    expect(post.headers.get('Idempotency-Key')).toBeTruthy();
  });
});
