import { act, fireEvent, render, screen } from '@testing-library/react';
import type { TFunction } from 'i18next';
import { describe, expect, it, vi } from 'vitest';

import { App } from '../App';
import { ApiError } from '../api/client';
import i18n from '../i18n';
import { isDefinite } from '../lib/pending';
import { errorBody, json, mockApi, scanResult, staffSession, txn } from '../test/api';
import { describeActionError } from './actionError';
import { Counter } from './Counter';

const SCAN = 'POST /v1/staff/scan';
const EARN = 'POST /v1/staff/points/earn';
const REDEEM = 'POST /v1/staff/points/redeem';

function lostResponse(): never {
  throw new TypeError('Failed to fetch');
}

async function scan() {
  fireEvent.click(screen.getByText('Type the code'));
  fireEvent.change(screen.getByLabelText('Card code'), { target: { value: 'ml1.code' } });
  fireEvent.click(screen.getByRole('button', { name: 'Look up' }));
  expect(await screen.findByText('Lin')).toBeInTheDocument();
  await act(async () => {});
}

async function earn(amount: string) {
  fireEvent.change(screen.getByLabelText('Purchase amount (NT$)'), { target: { value: amount } });
  fireEvent.click(screen.getByRole('button', { name: 'Add points' }));
}

// Security review Phase 3, F1: a write whose answer was lost keeps its key
// and body until a retry settles it.
describe('pending points write', () => {
  it('keeps the retry banner after the panel expires and resends the same key and body', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const api = mockApi({
      [SCAN]: () => json(200, scanResult(300)),
      [EARN]: (_call, n) => (n === 0 ? lostResponse() : json(200, txn(3, 123))),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    await earn('350');

    expect(await screen.findByText('Last action not confirmed')).toBeInTheDocument();
    expect(screen.getByText('Add points to Lin for NT$350')).toBeInTheDocument();

    act(() => vi.advanceTimersByTime(301_000));
    expect(screen.queryByTestId('balance')).not.toBeInTheDocument();
    expect(screen.getByText('Last action not confirmed')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('3 points added')).toBeInTheDocument();
    expect(screen.queryByText('Last action not confirmed')).not.toBeInTheDocument();

    const earns = api.to('POST', '/v1/staff/points/earn');
    expect(earns).toHaveLength(2);
    expect(earns[1]?.headers.get('Idempotency-Key')).toBe(earns[0]?.headers.get('Idempotency-Key'));
    expect(earns[1]?.body).toEqual(earns[0]?.body);
    expect(earns[0]?.body).toEqual({ scan_token: 'st1.scan-token', spend_amount: 350 });
  });

  it('blocks a new write for the same member until the pending one is settled', async () => {
    const api = mockApi({
      [SCAN]: () => json(200, scanResult(300)),
      [EARN]: () => json(503, errorBody('unavailable')),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    await earn('350');
    expect(await screen.findByText('Last action not confirmed')).toBeInTheDocument();

    // A new scan of the same member: the banner stays and the forms are blocked.
    fireEvent.click(screen.getByRole('button', { name: 'Done with this member' }));
    await scan();
    expect(screen.getByText(/has an unconfirmed action/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Purchase amount (NT$)'), { target: { value: '50' } });
    expect(screen.getByRole('button', { name: 'Add points' })).toBeDisabled();
    fireEvent.click(screen.getByRole('tab', { name: 'Redeem points' }));
    fireEvent.change(screen.getByLabelText('Points to redeem'), { target: { value: '10' } });
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled();
    expect(api.to('POST', '/v1/staff/points/earn')).toHaveLength(1);
    expect(api.to('POST', '/v1/staff/points/redeem')).toHaveLength(0);
  });

  it('discards only after a confirmation', async () => {
    mockApi({
      [SCAN]: () => json(200, scanResult(300)),
      [EARN]: (_call, n) =>
        n === 0 ? json(429, errorBody('rate_limited')) : json(200, txn(1, 121)),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    await earn('100');
    expect(await screen.findByText('Last action not confirmed')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(screen.getByText(/checking the member's points history/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Keep it' }));
    expect(screen.getByText('Last action not confirmed')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    fireEvent.click(screen.getByRole('button', { name: 'Discard it' }));
    expect(screen.queryByText('Last action not confirmed')).not.toBeInTheDocument();
    expect(screen.getByText('The unconfirmed action was discarded.')).toBeInTheDocument();
    expect(screen.queryByText(/has an unconfirmed action/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add points' })).toBeEnabled();
  });

  it('settles on a definite refusal without a banner', async () => {
    mockApi({
      [SCAN]: () => json(200, scanResult(300)),
      [REDEEM]: () => json(409, errorBody('scan_used', 'scan used')),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    fireEvent.click(screen.getByRole('tab', { name: 'Redeem points' }));
    fireEvent.change(screen.getByLabelText('Points to redeem'), { target: { value: '10' } });
    fireEvent.click(screen.getByRole('button', { name: 'Review' }));
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    expect(await screen.findByText(/already used for a redemption/)).toBeInTheDocument();
    expect(screen.queryByText('Last action not confirmed')).not.toBeInTheDocument();
  });

  it('clears the banner when the retry is refused for good', async () => {
    mockApi({
      [SCAN]: () => json(200, scanResult(300)),
      [EARN]: (_call, n) =>
        n === 0 ? lostResponse() : json(403, errorBody('scan_invalid', 'scan again')),
    });
    render(<Counter session={staffSession()} />);
    await scan();
    await earn('350');
    expect(await screen.findByText('Last action not confirmed')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText(/The last action was not applied/)).toBeInTheDocument();
    expect(screen.queryByText('Last action not confirmed')).not.toBeInTheDocument();
  });
});

describe('outcome classification', () => {
  it('treats only refusals that wrote nothing as definite', () => {
    const err = (status: number, code?: string) =>
      new ApiError(status, code ? { code, message: code } : undefined, 'x');
    expect(isDefinite(err(0))).toBe(false);
    expect(isDefinite(err(503))).toBe(false);
    expect(isDefinite(err(401))).toBe(false);
    expect(isDefinite(err(408))).toBe(false);
    expect(isDefinite(err(429))).toBe(false);
    expect(isDefinite(err(409, 'conflict'))).toBe(false);
    expect(isDefinite(err(409, 'insufficient_points'))).toBe(true);
    expect(isDefinite(err(409, 'scan_used'))).toBe(true);
    expect(isDefinite(err(403, 'self_service'))).toBe(true);
    expect(isDefinite(err(400))).toBe(true);
  });

  it('explains self-service and a used scan', () => {
    const t = i18n.t.bind(i18n) as TFunction;
    const self = describeActionError(new ApiError(403, { code: 'self_service' }, 'x'), t, 0);
    expect(self.message).toMatch(/your own member card/);
    expect(self.scanAgain).toBe(false);
    const used = describeActionError(new ApiError(409, { code: 'scan_used' }, 'x'), t, 0);
    expect(used.scanAgain).toBe(true);
    // The codes the server sends for an unusable scan token.
    for (const code of ['scan_invalid', 'scan_mismatch']) {
      expect(describeActionError(new ApiError(403, { code }, 'x'), t, 0).scanAgain).toBe(true);
    }
    // A role refusal is not a scan problem.
    const role = describeActionError(new ApiError(403, { code: 'forbidden' }, 'x'), t, 0);
    expect(role.scanAgain).toBe(false);
    expect(role.message).toMatch(/role does not allow/);
  });
});

// Security review Phase 3, F3: staff sign-in needs its own LINE Login channel.
describe('staff channel', () => {
  it('explains a missing staff channel instead of a server error', async () => {
    mockApi({
      'POST /v1/staff/auth/line': () =>
        json(409, errorBody('staff_channel_required', 'no staff channel')),
    });
    render(<App />);
    expect(await screen.findByText(/LINE Login channel is not set up/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
  });
});
