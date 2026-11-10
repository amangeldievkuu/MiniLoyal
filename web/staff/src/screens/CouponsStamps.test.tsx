import { act, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import type { ScanResult, StampResult } from '../api/types';
import { coupon, errorBody, json, mockApi, scanResult, stampCard, staffSession } from '../test/api';
import { Counter } from './Counter';

const SCAN = 'POST /v1/staff/scan';
const STAMPS = 'POST /v1/staff/stamps';

const latte = coupon('c1', 'Free latte', '2027-01-31T00:00:00Z');
const tea = coupon('c2', 'Free tea');
const redeemPath = (c: { id: string }) => `/v1/staff/coupons/${c.id}/redeem`;
const REDEEM = (c: { id: string }) => `POST ${redeemPath(c)}`;
const redeemed = (c: typeof latte) => ({ ...c, status: 'redeemed', state: 'redeemed' });

function scanWith(over: Partial<ScanResult>): ScanResult {
  return { ...scanResult(300), ...over };
}

async function scan() {
  fireEvent.click(screen.getByText('Type the code'));
  fireEvent.change(screen.getByLabelText('Card code'), { target: { value: 'ml1.code' } });
  fireEvent.click(screen.getByRole('button', { name: 'Look up' }));
  expect(await screen.findByText('Lin')).toBeInTheDocument();
  await act(async () => {});
}

function lost(): never {
  throw new TypeError('Failed to fetch');
}

async function redeem(name: string) {
  fireEvent.click(screen.getByRole('tab', { name: 'Coupons' }));
  fireEvent.click(screen.getByRole('button', { name: `Redeem ${name}` }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm redeem' }));
}

describe('coupons', () => {
  it('lists coupons and redeems several on one scan', async () => {
    const api = mockApi({
      [SCAN]: () => json(200, scanWith({ coupons: [latte, tea] })),
      [REDEEM(latte)]: () => json(200, redeemed(latte)),
      [REDEEM(tea)]: () => json(200, redeemed(tea)),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    fireEvent.click(screen.getByRole('tab', { name: 'Coupons' }));
    expect(screen.getByText('No expiry')).toBeInTheDocument();
    expect(screen.getByText(/Expires/)).toBeInTheDocument();

    // Asks before sending.
    fireEvent.click(screen.getByRole('button', { name: 'Redeem Free latte' }));
    expect(api.calls.filter((c) => c.path.includes('redeem'))).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: 'Confirm redeem' }));
    expect(await screen.findByText('Redeemed: Free latte')).toBeInTheDocument();

    // The scan is still usable: redeem a second coupon.
    fireEvent.click(screen.getByRole('button', { name: 'Redeem Free tea' }));
    fireEvent.click(screen.getByRole('button', { name: 'Confirm redeem' }));
    expect(await screen.findByText('Redeemed: Free tea')).toBeInTheDocument();
    expect(api.to('POST', redeemPath(latte))[0]?.body).toEqual({ scan_token: 'st1.scan-token' });
    expect(api.to('POST', redeemPath(latte))[0]?.headers.get('Idempotency-Key')).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Coupons' })).toBeInTheDocument();
  });

  it.each([
    [409, 'coupon_unavailable', /already used, has expired, or belongs to another member/, false],
    [403, 'scan_invalid', /no longer valid/, true],
    [403, 'scan_mismatch', /no longer valid/, true],
    [403, 'self_service', /your own member card/, false],
  ])('shows %i %s', async (status, code, text, scanAgain) => {
    mockApi({
      [SCAN]: () => json(200, scanWith({ coupons: [latte] })),
      [REDEEM(latte)]: () => json(status, errorBody(code)),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    await redeem('Free latte');
    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.queryByText('Last action not confirmed')).not.toBeInTheDocument();
    const again = screen.queryByRole('button', { name: 'Scan member card' });
    expect(again !== null).toBe(scanAgain);
  });

  it('keeps a rate-limited redeem pending and retries with the same key', async () => {
    const api = mockApi({
      [SCAN]: () => json(200, scanWith({ coupons: [latte] })),
      [REDEEM(latte)]: (_c, n) =>
        n === 0
          ? json(429, errorBody('rate_limited'), { 'Retry-After': '7' })
          : json(200, redeemed(latte)),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    await redeem('Free latte');
    expect(await screen.findByText('Last action not confirmed')).toBeInTheDocument();
    expect(screen.getByText(/Wait 7 s/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('Redeemed: Free latte')).toBeInTheDocument();
    const calls = api.to('POST', redeemPath(latte));
    expect(calls).toHaveLength(2);
    expect(calls[1]?.headers.get('Idempotency-Key')).toBe(calls[0]?.headers.get('Idempotency-Key'));
  });

  it('keeps a dropped redeem in memory and retries with the same key', async () => {
    const api = mockApi({
      [SCAN]: () => json(200, scanWith({ coupons: [latte] })),
      [REDEEM(latte)]: (_c, n) => (n === 0 ? lost() : json(200, redeemed(latte))),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    await redeem('Free latte');
    expect(await screen.findByText('Last action not confirmed')).toBeInTheDocument();
    expect(screen.getByText('Redeem coupon Free latte for Lin')).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Redeem Free latte' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('Redeemed: Free latte')).toBeInTheDocument();
    expect(screen.queryByText('Last action not confirmed')).not.toBeInTheDocument();
    const calls = api.to('POST', redeemPath(latte));
    expect(calls[1]?.headers.get('Idempotency-Key')).toBe(calls[0]?.headers.get('Idempotency-Key'));
    expect(calls[1]?.body).toEqual(calls[0]?.body);
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain('st1.');
    expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain('st1.');
  });
});

function stampResult(over: Partial<StampResult> & { count: number; added: number }): StampResult {
  const { count, ...rest } = over;
  return {
    progress: { ...stampCard(), count },
    completed: 0,
    rewards: [],
    rewards_unavailable: 0,
    ...rest,
  };
}

function openStamps() {
  fireEvent.click(screen.getByRole('tab', { name: 'Stamps' }));
}

describe('stamps', () => {
  const card = stampCard(3, 3);
  const withCard = () => scanWith({ stamps: [card] });

  it('shows progress and adds stamps with the API result', async () => {
    const api = mockApi({
      [SCAN]: () => json(200, withCard()),
      [STAMPS]: () => json(200, stampResult({ count: 4, added: 1 })),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    openStamps();
    expect(screen.getByText('3 of 5 stamps')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Add 1 stamp(s) to Coffee card' }));
    expect(await screen.findByText('1 stamp(s) added.')).toBeInTheDocument();
    expect(screen.getByText('4 of 5 stamps')).toBeInTheDocument();
    expect(api.to('POST', '/v1/staff/stamps')[0]?.body).toEqual({
      scan_token: 'st1.scan-token',
      stamp_card_id: card.stamp_card_id,
      count: 1,
    });
  });

  it('caps the stepper at max_stamps_per_visit across adds on one scan', async () => {
    mockApi({
      [SCAN]: () => json(200, scanWith({ stamps: [stampCard(0, 3)] })),
      [STAMPS]: () => json(200, stampResult({ count: 2, added: 2 })),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    openStamps();
    const more = screen.getByRole('button', { name: 'More stamps' });
    fireEvent.click(more);
    fireEvent.click(more);
    expect(more).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Fewer stamps' }));
    fireEvent.click(screen.getByRole('button', { name: /^Add 2 stamp/ }));
    expect(await screen.findByText('2 stamp(s) added.')).toBeInTheDocument();
    // One stamp left on this visit.
    expect(screen.getByRole('button', { name: 'More stamps' })).toBeDisabled();
    expect(screen.getByText(/Up to 1 more on this visit/)).toBeInTheDocument();
  });

  it('shows the reward coupon on completion', async () => {
    mockApi({
      [SCAN]: () => json(200, withCard()),
      [STAMPS]: () =>
        json(
          200,
          stampResult({ count: 0, added: 2, completed: 1, rewards: [coupon('c9', 'Free cake')] }),
        ),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    openStamps();
    fireEvent.click(screen.getByRole('button', { name: 'More stamps' }));
    fireEvent.click(screen.getByRole('button', { name: /^Add 2 stamp/ }));
    expect(await screen.findByText('Reward coupon issued: Free cake')).toBeInTheDocument();
    expect(screen.getByText('Card completed (1x).')).toBeInTheDocument();
    expect(screen.queryByText(/not available/)).not.toBeInTheDocument();
  });

  it('says so when the reward is unavailable', async () => {
    mockApi({
      [SCAN]: () => json(200, withCard()),
      [STAMPS]: () =>
        json(200, stampResult({ count: 0, added: 2, completed: 1, rewards_unavailable: 1 })),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    openStamps();
    fireEvent.click(screen.getByRole('button', { name: 'More stamps' }));
    fireEvent.click(screen.getByRole('button', { name: /^Add 2 stamp/ }));
    const warning = await screen.findByText(/reward is not available right now/);
    expect(warning).toHaveTextContent(/contact the brand/);
    expect(screen.queryByText(/Reward coupon issued/)).not.toBeInTheDocument();
  });

  it.each([
    ['stamp_visit_limit', 409, /No more stamps can be given on this visit/],
    ['stamp_card_inactive', 409, /no longer active/],
    ['scan_invalid', 403, /no longer valid/],
    ['self_service', 403, /your own member card/],
  ])('shows %s', async (code, status, text) => {
    mockApi({
      [SCAN]: () => json(200, withCard()),
      [STAMPS]: () => json(status, errorBody(code)),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    openStamps();
    fireEvent.click(screen.getByRole('button', { name: /^Add 1 stamp/ }));
    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.queryByText('Last action not confirmed')).not.toBeInTheDocument();
  });

  it('retries a dropped add with the same key and body', async () => {
    const api = mockApi({
      [SCAN]: () => json(200, withCard()),
      [STAMPS]: (_c, n) => (n === 0 ? lost() : json(200, stampResult({ count: 4, added: 1 }))),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    openStamps();
    fireEvent.click(screen.getByRole('button', { name: /^Add 1 stamp/ }));
    expect(await screen.findByText('Last action not confirmed')).toBeInTheDocument();
    expect(screen.getByText('Add 1 stamp(s) on Coffee card for Lin')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('4 of 5 stamps')).toBeInTheDocument();
    const calls = api.to('POST', '/v1/staff/stamps');
    expect(calls).toHaveLength(2);
    expect(calls[1]?.headers.get('Idempotency-Key')).toBe(calls[0]?.headers.get('Idempotency-Key'));
    expect(calls[1]?.body).toEqual(calls[0]?.body);
  });
});
