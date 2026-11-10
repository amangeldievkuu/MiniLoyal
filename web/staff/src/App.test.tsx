import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { App } from './App';
import { APP_ID, errorBody, json, mockApi, scanResult, staffSession, txn } from './test/api';
import { liffMock } from './test/liff';

const AUTH = 'POST /v1/staff/auth/line';
const SCAN = 'POST /v1/staff/scan';
const EARN = 'POST /v1/staff/points/earn';
const REDEEM = 'POST /v1/staff/points/redeem';

/** Signs in (no invite needed) and scans a card through the typed-code fallback. */
async function signInAndScan(user: ReturnType<typeof userEvent.setup>, balance = 120) {
  render(<App />);
  await screen.findByText('Mei');
  await user.click(screen.getByText('Type the code'));
  await user.type(screen.getByLabelText('Card code'), 'ml1.typed-code');
  await user.click(screen.getByRole('button', { name: 'Look up' }));
  await screen.findByText('Lin');
  expect(screen.getByTestId('balance')).toHaveTextContent(String(balance));
}

describe('sign-in', () => {
  it('asks for the invite code on the first sign-in and binds the LINE account', async () => {
    const user = userEvent.setup();
    const api = mockApi({
      [AUTH]: (call) =>
        call.body?.invite_code
          ? json(200, staffSession())
          : json(401, errorBody('unauthorized', 'unknown staff')),
    });
    render(<App />);

    const input = await screen.findByLabelText('Invite code');
    await user.type(input, 'abcd-efgh jkmn');
    await user.click(screen.getByRole('button', { name: 'Link and sign in' }));

    expect(await screen.findByText('Mei')).toBeInTheDocument();
    expect(screen.getByText('Zhongxiao')).toBeInTheDocument();
    const auth = api.to('POST', '/v1/staff/auth/line');
    expect(auth).toHaveLength(2);
    expect(auth[0]?.body).toEqual({ app_id: APP_ID, id_token: 'line-id-token' });
    expect(auth[1]?.body).toEqual({
      app_id: APP_ID,
      id_token: 'line-id-token',
      invite_code: 'ABCDEFGHJKMN',
    });
    expect(liffMock.init).toHaveBeenCalledWith({ liffId: '2000000000-staffapp' });
  });

  it('shows a wrong invite code and the wait after too many attempts', async () => {
    const user = userEvent.setup();
    mockApi({
      [AUTH]: (call, n) =>
        !call.body?.invite_code
          ? json(401, errorBody('unauthorized'))
          : n === 1
            ? json(401, errorBody('unauthorized'))
            : json(
                429,
                errorBody('rate_limited', 'too many requests', { retry_after_seconds: 42 }),
              ),
    });
    render(<App />);
    const input = await screen.findByLabelText('Invite code');
    await user.type(input, 'WRONGCODE111');
    await user.click(screen.getByRole('button', { name: 'Link and sign in' }));
    expect(await screen.findByText(/wrong or expired/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Link and sign in' }));
    expect(await screen.findByText(/Try again in 42 s/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Link and sign in' })).toBeDisabled();
  });

  it('sends a cashier who is not logged in to LINE login', async () => {
    liffMock.isLoggedIn.mockReturnValue(false);
    const api = mockApi({});
    render(<App />);
    expect(await screen.findByText('Opening LINE login…')).toBeInTheDocument();
    expect(liffMock.login).toHaveBeenCalledOnce();
    expect(api.calls).toHaveLength(0);
  });

  it('re-exchanges a fresh ID token once after a 401 and retries the request', async () => {
    const user = userEvent.setup();
    const api = mockApi({
      [AUTH]: (_call, n) => json(200, staffSession('cashier', `staff-token-${n + 1}`)),
      [SCAN]: (call) =>
        call.headers.get('Authorization') === 'Bearer staff-token-2'
          ? json(200, scanResult())
          : json(401, errorBody('unauthorized', 'session expired')),
    });
    await signInAndScan(user);

    expect(api.to('POST', '/v1/staff/auth/line')).toHaveLength(2);
    const scans = api.to('POST', '/v1/staff/scan');
    expect(scans.map((c) => c.headers.get('Authorization'))).toEqual([
      'Bearer staff-token-1',
      'Bearer staff-token-2',
    ]);
    expect(scans[1]?.body).toEqual({ qr: 'ml1.typed-code' });
  });

  it('ends the session when the re-exchange cannot fix a 401', async () => {
    const user = userEvent.setup();
    mockApi({
      [AUTH]: (_call, n) =>
        n === 0 ? json(200, staffSession()) : json(401, errorBody('unauthorized')),
      [SCAN]: () => json(401, errorBody('unauthorized')),
    });
    render(<App />);
    await screen.findByText('Mei');
    await user.click(screen.getByText('Type the code'));
    await user.type(screen.getByLabelText('Card code'), 'ml1.code');
    await user.click(screen.getByRole('button', { name: 'Look up' }));
    expect(await screen.findByText('Your session ended. Sign in again.')).toBeInTheDocument();
  });
});

describe('scanning', () => {
  it('reads the card with scanCodeV2 and shows the member', async () => {
    const user = userEvent.setup();
    liffMock.isApiAvailable.mockReturnValue(true);
    liffMock.scanCodeV2.mockResolvedValue({ value: 'ml1.native' });
    const api = mockApi({
      [AUTH]: () => json(200, staffSession()),
      [SCAN]: () => json(200, scanResult()),
    });
    render(<App />);
    await screen.findByText('Mei');
    await user.click(screen.getByRole('button', { name: 'Scan member card' }));

    expect(await screen.findByText('Lin')).toBeInTheDocument();
    expect(screen.getByText('Friend')).toBeInTheDocument();
    expect(screen.getByTestId('balance')).toHaveTextContent('120');
    expect(liffMock.isApiAvailable).toHaveBeenCalledWith('scanCodeV2');
    expect(api.to('POST', '/v1/staff/scan')[0]?.body).toEqual({ qr: 'ml1.native' });
  });

  it('asks the member to refresh an expired card', async () => {
    const user = userEvent.setup();
    liffMock.isApiAvailable.mockReturnValue(true);
    liffMock.scanCodeV2.mockResolvedValue({ value: 'ml1.screenshot' });
    mockApi({
      [AUTH]: () => json(200, staffSession()),
      [SCAN]: () => json(422, errorBody('card_expired', 'card code expired')),
    });
    render(<App />);
    await screen.findByText('Mei');
    await user.click(screen.getByRole('button', { name: 'Scan member card' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/refresh their card/);
    expect(screen.queryByText('Lin')).not.toBeInTheDocument();
  });
});

describe('earning', () => {
  it('sends an Idempotency-Key and reuses it when retrying after a failure', async () => {
    const user = userEvent.setup();
    const api = mockApi({
      [AUTH]: () => json(200, staffSession()),
      [SCAN]: () => json(200, scanResult()),
      [EARN]: (_call, n) =>
        n === 0 ? json(503, errorBody('unavailable')) : json(200, txn(3, 123)),
    });
    await signInAndScan(user);

    await user.type(screen.getByLabelText('Purchase amount (NT$)'), '350');
    await user.click(screen.getByRole('button', { name: 'Add points' }));
    expect(await screen.findByText(/points will not be counted twice/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Try again' }));

    expect(await screen.findByText('3 points added')).toBeInTheDocument();
    expect(screen.getByText('New balance: 123 points')).toBeInTheDocument();
    const earns = api.to('POST', '/v1/staff/points/earn');
    expect(earns).toHaveLength(2);
    const key = earns[0]?.headers.get('Idempotency-Key');
    expect(key).toMatch(/^[0-9a-f-]{36}$/);
    expect(earns[1]?.headers.get('Idempotency-Key')).toBe(key);
    expect(earns[0]?.body).toEqual({ scan_token: 'st1.scan-token', spend_amount: 350 });

    await user.click(screen.getByRole('button', { name: 'Next customer' }));
    expect(screen.getByRole('button', { name: 'Scan member card' })).toBeInTheDocument();
  });

  it('shows the fixed-points toggle to managers only', async () => {
    const user = userEvent.setup();
    mockApi({
      [AUTH]: () => json(200, staffSession('cashier')),
      [SCAN]: () => json(200, scanResult()),
    });
    await signInAndScan(user);
    expect(screen.queryByLabelText(/fixed number of points/)).not.toBeInTheDocument();
  });

  it('lets a manager give a fixed number of points', async () => {
    const user = userEvent.setup();
    const api = mockApi({
      [AUTH]: () => json(200, staffSession('manager')),
      [SCAN]: () => json(200, scanResult()),
      [EARN]: () => json(200, txn(50, 170)),
    });
    await signInAndScan(user);
    expect(screen.getByText('Manager')).toBeInTheDocument();
    await user.click(screen.getByLabelText(/fixed number of points/));
    await user.type(screen.getByLabelText('Points'), '50');
    await user.click(screen.getByRole('button', { name: 'Add points' }));
    expect(await screen.findByText('50 points added')).toBeInTheDocument();
    expect(api.to('POST', '/v1/staff/points/earn')[0]?.body).toEqual({
      scan_token: 'st1.scan-token',
      points: 50,
    });
  });

  it('tells the cashier to scan again when the scan token is refused', async () => {
    const user = userEvent.setup();
    mockApi({
      [AUTH]: () => json(200, staffSession()),
      [SCAN]: () => json(200, scanResult()),
      [EARN]: () => json(403, errorBody('scan_mismatch', 'scan again')),
    });
    await signInAndScan(user);
    await user.type(screen.getByLabelText('Purchase amount (NT$)'), '100');
    await user.click(screen.getByRole('button', { name: 'Add points' }));
    expect(await screen.findByText(/Scan the member card again/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add points' })).toBeDisabled();
  });
});

describe('redeeming', () => {
  it('blocks more than the balance and shows a 409 from the server', async () => {
    const user = userEvent.setup();
    const api = mockApi({
      [AUTH]: () => json(200, staffSession()),
      [SCAN]: () => json(200, scanResult(300, 120)),
      [REDEEM]: () =>
        json(409, errorBody('insufficient_points', 'not enough points', { balance: 80 })),
    });
    await signInAndScan(user);
    await user.click(screen.getByRole('tab', { name: 'Redeem points' }));

    const input = screen.getByLabelText('Points to redeem');
    await user.type(input, '500');
    expect(screen.getByText('More than the balance of 120 points.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled();

    await user.clear(input);
    await user.type(input, '100');
    await user.click(screen.getByRole('button', { name: 'Review' }));
    expect(screen.getByText('Redeem 100 points? The new balance will be 20.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('Not enough points. Balance: 80.')).toBeInTheDocument();
    const redeems = api.to('POST', '/v1/staff/points/redeem');
    expect(redeems).toHaveLength(1);
    expect(redeems[0]?.body).toEqual({ scan_token: 'st1.scan-token', points: 100 });
    expect(redeems[0]?.headers.get('Idempotency-Key')).toBeTruthy();
  });

  it('confirms a redemption and shows the new balance', async () => {
    const user = userEvent.setup();
    mockApi({
      [AUTH]: () => json(200, staffSession()),
      [SCAN]: () => json(200, scanResult(300, 120)),
      [REDEEM]: () => json(200, txn(-100, 20)),
    });
    await signInAndScan(user);
    await user.click(screen.getByRole('tab', { name: 'Redeem points' }));
    await user.type(screen.getByLabelText('Points to redeem'), '100');
    await user.click(screen.getByRole('button', { name: 'Review' }));
    await user.click(screen.getByRole('button', { name: 'Confirm' }));
    const status = await screen.findByRole('status');
    expect(within(status).getByText('100 points redeemed')).toBeInTheDocument();
    expect(within(status).getByText('New balance: 20 points')).toBeInTheDocument();
  });
});
