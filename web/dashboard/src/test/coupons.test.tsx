import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { CouponInstance, CouponTemplate } from '../api/types';
import {
  emptyDraft,
  toCreateBody,
  toDraft,
  toUpdateBody,
  validateCoupon,
} from '../features/coupons/couponForm';
import { fromLocalInput, toLocalInput } from '../lib/datetime';
import { CouponTemplatesView } from '../pages/CouponTemplatesPage';
import { MemberCouponsPanel } from '../pages/MemberCoupons';
import { jsonResponse, renderWithProviders, stubFetch, type RecordedCall } from './providers';

afterEach(() => vi.unstubAllGlobals());

const dialogButton = (name: string) => {
  const btn = screen.getAllByRole('button', { name, hidden: true }).at(-1);
  if (!btn) throw new Error(`button ${name} not rendered`);
  return btn;
};

const tpl = (extra: Partial<CouponTemplate> = {}): CouponTemplate => ({
  id: 'c1',
  app_id: 'a1',
  name: 'Weekday deal',
  description: 'Weekdays',
  type: 'amount_off',
  value: 50,
  total_limit: 100,
  per_member_limit: 1,
  valid_from: null,
  valid_to: null,
  validity_days: 30,
  claimable: true,
  claimed_count: 0,
  status: 'active',
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
  ...extra,
});

const apiError = (status: number, code: string, details?: Record<string, string>) =>
  jsonResponse(status, { code, message: `msg ${code}`, request_id: 'r1', details });

describe('coupon form logic', () => {
  it('validates value against type and limits', () => {
    const base = { ...emptyDraft, name: 'x', value: '50' };
    expect(validateCoupon(base)).toEqual({});
    expect(validateCoupon({ ...base, name: ' ' }).name).toBe('nameRequired');
    expect(validateCoupon({ ...base, type: 'percent_off', value: '101' }).value).toBe(
      'percentValue',
    );
    expect(validateCoupon({ ...base, value: '' }).value).toBe('amountValue');
    expect(validateCoupon({ ...base, type: 'free_item', value: '' })).toEqual({});
    expect(
      validateCoupon({ ...base, total_limit: '2', per_member_limit: '3' }).per_member_limit,
    ).toBe('perMemberAboveTotal');
    expect(
      validateCoupon({ ...base, valid_from: '2026-11-10T10:00', valid_to: '2026-11-10T09:00' })
        .valid_to,
    ).toBe('windowOrder');
  });

  it('omits value for free_item on create', () => {
    const body = toCreateBody({ ...emptyDraft, name: 'Tea', type: 'free_item', value: '9' });
    expect(body).toEqual({ name: 'Tea', type: 'free_item', per_member_limit: 1, claimable: false });
  });

  it('sends only changes and clears emptied optional fields', () => {
    const original = tpl({ valid_to: '2026-12-31T00:00:00Z' });
    const draft = {
      ...toDraft(original),
      description: '',
      total_limit: '',
      valid_to: '',
      name: 'Renamed',
    };
    expect(toUpdateBody(draft, original, false)).toEqual({
      name: 'Renamed',
      clear: ['description', 'total_limit', 'valid_to'],
    });
  });

  it('never sends type or value when locked, and clears value for free_item', () => {
    const original = tpl();
    const edited = { ...toDraft(original), value: '70', name: 'N' };
    expect(toUpdateBody(edited, original, true)).toEqual({ name: 'N' });
    const free = { ...toDraft(original), type: 'free_item' as const };
    expect(toUpdateBody(free, original, false)).toEqual({ type: 'free_item', clear: ['value'] });
  });

  it('round-trips datetime-local values', () => {
    const iso = '2026-11-10T03:00:00.000Z';
    expect(fromLocalInput(toLocalInput(iso))).toBe(iso);
    expect(fromLocalInput('')).toBeNull();
    expect(toLocalInput(null)).toBe('');
  });
});

describe('CouponTemplatesView', () => {
  const route = (list: CouponTemplate[], onWrite?: (c: RecordedCall) => Response | undefined) =>
    stubFetch((call) => {
      if (call.path === '/v1/dashboard/apps/a1/coupon-templates' && call.method === 'GET') {
        const status = call.query.get('status');
        return jsonResponse(200, status ? list.filter((x) => x.status === status) : list);
      }
      return onWrite?.(call);
    });

  it('lists templates with claimed counts and filters by status', async () => {
    const { calls } = route([
      tpl({ claimed_count: 7 }),
      tpl({ id: 'c2', name: 'Free tea', type: 'free_item', value: null, status: 'archived' }),
    ]);
    const user = userEvent.setup();
    renderWithProviders(<CouponTemplatesView appId="a1" role="owner" />);

    expect(await screen.findByText('Weekday deal')).toBeInTheDocument();
    expect(screen.getByText('7 / 100')).toBeInTheDocument();
    expect(screen.getAllByText('Free item')).not.toHaveLength(0);

    await user.selectOptions(screen.getByLabelText('Show'), 'archived');
    await waitFor(() => expect(screen.queryByText('Weekday deal')).not.toBeInTheDocument());
    expect(calls.some((c) => c.query.get('status') === 'archived')).toBe(true);
  });

  it('is read only for the staff role', async () => {
    route([tpl()]);
    renderWithProviders(<CouponTemplatesView appId="a1" role="staff" />);
    expect(await screen.findByText('Weekday deal')).toBeInTheDocument();
    expect(screen.getByText('Your role can only view this page.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New template' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Archive/ })).not.toBeInTheDocument();
  });

  it('refuses an invalid form and creates with an Idempotency-Key', async () => {
    const { calls } = route([], (c) =>
      c.method === 'POST' ? jsonResponse(201, tpl({ id: 'new', name: 'Five off' })) : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<CouponTemplatesView appId="a1" role="admin" />);

    await user.click(await screen.findByRole('button', { name: 'New template' }));
    await user.click(dialogButton('Save'));
    expect(await screen.findByText('Enter a name.')).toBeInTheDocument();
    expect(screen.getByText('Enter a whole number from 1 to 1,000,000.')).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);

    await user.type(screen.getByLabelText('Name'), 'Five off');
    await user.type(screen.getByLabelText('Amount off (NT$)'), '5');
    await user.click(dialogButton('Save'));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST') as RecordedCall;
    expect(post.headers.get('Idempotency-Key')).toBeTruthy();
    expect(post.body).toEqual({
      name: 'Five off',
      type: 'amount_off',
      value: 5,
      per_member_limit: 1,
      claimable: false,
    });
  });

  it('hides the value for free_item', async () => {
    route([]);
    const user = userEvent.setup();
    renderWithProviders(<CouponTemplatesView appId="a1" role="owner" />);
    await user.click(await screen.findByRole('button', { name: 'New template' }));
    expect(screen.getByLabelText('Amount off (NT$)')).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Type'), 'free_item');
    expect(screen.queryByLabelText('Amount off (NT$)')).not.toBeInTheDocument();
  });

  it('locks type and value once coupons were issued and PATCHes only changes', async () => {
    const { calls } = route([tpl({ claimed_count: 3 })], (c) =>
      c.method === 'PATCH'
        ? jsonResponse(200, tpl({ name: 'New name', claimed_count: 3 }))
        : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<CouponTemplatesView appId="a1" role="owner" />);

    await user.click(await screen.findByRole('button', { name: 'Edit: Weekday deal' }));
    expect(screen.getByLabelText('Type')).toBeDisabled();
    expect(screen.getByLabelText('Amount off (NT$)')).toBeDisabled();
    expect(dialogButton('Save')).toBeDisabled();

    await user.clear(screen.getByLabelText('Name'));
    await user.type(screen.getByLabelText('Name'), 'New name');
    await user.clear(screen.getByLabelText('Total limit'));
    await user.click(dialogButton('Save'));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH') as RecordedCall;
    expect(patch.path).toBe('/v1/dashboard/coupon-templates/c1');
    expect(patch.body).toEqual({ name: 'New name', clear: ['total_limit'] });
    expect(patch.headers.get('Idempotency-Key')).toBeTruthy();
  });

  it('shows 422 details.total_limit on the field and 409 in_use on the type', async () => {
    let mode: 'limit' | 'inuse' = 'limit';
    route([tpl()], (c) =>
      c.method === 'PATCH'
        ? mode === 'limit'
          ? apiError(422, 'validation_failed', { total_limit: 'Cannot be below the 5 issued.' })
          : apiError(409, 'coupon_template_in_use')
        : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<CouponTemplatesView appId="a1" role="owner" />);

    await user.click(await screen.findByRole('button', { name: 'Edit: Weekday deal' }));
    await user.clear(screen.getByLabelText('Total limit'));
    await user.type(screen.getByLabelText('Total limit'), '2');
    await user.click(dialogButton('Save'));
    expect(await screen.findByText('Cannot be below the 5 issued.')).toBeInTheDocument();
    expect(screen.getByLabelText('Total limit')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.queryByText('msg validation_failed')).not.toBeInTheDocument();

    mode = 'inuse';
    await user.clear(screen.getByLabelText('Amount off (NT$)'));
    await user.type(screen.getByLabelText('Amount off (NT$)'), '60');
    await user.click(dialogButton('Save'));
    expect(await screen.findByText(/can no longer change/, { selector: 'p' })).toBeInTheDocument();
    expect(screen.getByLabelText('Type')).toBeDisabled();
  });

  it('reuses the key on a retry of the same body and changes it for another', async () => {
    let fail = true;
    const { calls } = route([tpl()], (c) => {
      if (c.method !== 'PATCH') return undefined;
      if (fail) return apiError(500, 'internal');
      return jsonResponse(200, tpl({ status: 'archived' }));
    });
    const user = userEvent.setup();
    renderWithProviders(<CouponTemplatesView appId="a1" role="owner" />);

    const archive = await screen.findByRole('button', { name: 'Archive: Weekday deal' });
    await user.click(archive);
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1));
    fail = false;
    await user.click(await screen.findByRole('button', { name: 'Archive: Weekday deal' }));
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(2));
    const patches = calls.filter((c) => c.method === 'PATCH');
    expect(patches[0]?.body).toEqual({ status: 'archived' });
    expect(patches[0]?.headers.get('Idempotency-Key')).toBe(
      patches[1]?.headers.get('Idempotency-Key'),
    );
  });
});

const instance = (extra: Partial<CouponInstance> = {}): CouponInstance => ({
  id: 'i1',
  code: 'ABC123',
  template: { id: 'c1', name: 'Weekday deal', type: 'amount_off', value: 50 },
  member_id: 'm1',
  status: 'issued',
  state: 'available',
  source: 'issue',
  issued_at: '2026-11-05T00:00:00Z',
  expires_at: null,
  ...extra,
});

describe('MemberCouponsPanel', () => {
  const routes = (onPost?: (c: RecordedCall) => Response | undefined) =>
    stubFetch((call) => {
      if (call.method === 'POST') return onPost?.(call);
      if (call.path === '/v1/dashboard/members/m1/coupons') {
        const state = call.query.get('state');
        const all = [
          instance(),
          instance({ id: 'i2', code: 'VOID99', status: 'voided', state: 'voided' }),
        ];
        return jsonResponse(200, {
          items: state ? all.filter((x) => x.state === state) : all,
          next_cursor: null,
        });
      }
      if (call.path === '/v1/dashboard/apps/a1/coupon-templates') {
        return jsonResponse(200, [tpl()]);
      }
      return undefined;
    });

  it('filters by state and hides writes from staff', async () => {
    const { calls } = routes();
    const user = userEvent.setup();
    renderWithProviders(<MemberCouponsPanel appId="a1" memberId="m1" editable={false} />);

    expect(await screen.findByText('ABC123')).toBeInTheDocument();
    expect(screen.getByText('VOID99')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Issue coupon' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Void/ })).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('State'), 'voided');
    await waitFor(() => expect(screen.queryByText('ABC123')).not.toBeInTheDocument());
    expect(calls.some((c) => c.query.get('state') === 'voided')).toBe(true);
  });

  it('issues from a template and maps coupon_sold_out', async () => {
    const { calls } = routes((c) =>
      c.path === '/v1/dashboard/members/m1/coupons' ? apiError(409, 'coupon_sold_out') : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<MemberCouponsPanel appId="a1" memberId="m1" editable />);

    await user.click(await screen.findByRole('button', { name: 'Issue coupon' }));
    expect(dialogButton('Issue')).toBeDisabled();
    await user.selectOptions(await screen.findByLabelText('Template'), 'c1');
    await user.click(dialogButton('Issue'));

    expect(
      await screen.findByText('This template has reached its total limit.'),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.method === 'POST') as RecordedCall;
    expect(post.body).toEqual({ template_id: 'c1' });
    expect(post.headers.get('Idempotency-Key')).toBeTruthy();
  });

  it('voids with a reason, only for issued coupons', async () => {
    const { calls } = routes((c) =>
      c.path === '/v1/dashboard/coupons/i1/void'
        ? jsonResponse(200, instance({ status: 'voided', state: 'voided' }))
        : undefined,
    );
    const user = userEvent.setup();
    renderWithProviders(<MemberCouponsPanel appId="a1" memberId="m1" editable />);

    const row = (await screen.findByText('ABC123')).closest('tr') as HTMLElement;
    expect(
      within(screen.getByText('VOID99').closest('tr') as HTMLElement).queryByRole('button'),
    ).toBeNull();
    await user.click(within(row).getByRole('button', { name: 'Void: ABC123' }));
    expect(dialogButton('Void')).toBeDisabled();
    await user.type(screen.getByLabelText('Reason'), 'Printed twice');
    await user.click(dialogButton('Void'));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST') as RecordedCall;
    expect(post.body).toEqual({ reason: 'Printed twice' });
    expect(post.headers.get('Idempotency-Key')).toBeTruthy();
  });
});
