import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { pressKey } from '../components/Keypad';
import { sendPointsWrite, type EarnBody, type RedeemBody } from '../lib/pending';
import { json, mockApi, scanResult, txn } from '../test/api';
import { EarnForm } from './EarnForm';
import { RedeemForm } from './RedeemForm';

const EARN = 'POST /v1/staff/points/earn';
const REDEEM = 'POST /v1/staff/points/redeem';

/** The largest spend_amount and points the API accepts (api/openapi.yaml). */
const MAX_SPEND = 100_000_000;
const MAX_POINTS = 1_000_000;

// The counter owns submission (it holds the Idempotency-Key until the
// outcome is known); these tests submit directly through the same path.
const member = scanResult().member;
const earnProps = {
  blocked: false,
  onSubmit: (body: EarnBody) =>
    sendPointsWrite({ kind: 'earn', body, key: crypto.randomUUID(), staffId: 'staff', member }),
};
const redeemProps = {
  blocked: false,
  onSubmit: (body: RedeemBody) =>
    sendPointsWrite({ kind: 'redeem', body, key: crypto.randomUUID(), staffId: 'staff', member }),
};

describe('keypad leading zeros', () => {
  it('never builds a number with a leading zero from the keys', () => {
    expect(pressKey('', '0')).toBe('0');
    expect(pressKey('0', '0')).toBe('0');
    expect(pressKey('0', '00')).toBe('0');
    expect(pressKey('', '00')).toBe('0');
    expect(pressKey('0', '7')).toBe('7');
    expect(pressKey('0', 'back')).toBe('');
    expect(pressKey('', 'back')).toBe('');
    expect(pressKey('10', '0')).toBe('100');
  });

  it('turns a typed amount with leading zeros into the plain number', async () => {
    const user = userEvent.setup();
    const api = mockApi({ [EARN]: () => json(200, txn(5, 125)) });
    const onDone = vi.fn();
    render(
      <EarnForm
        {...earnProps}
        scan={scanResult()}
        isManager={false}
        onDone={onDone}
        onScanAgain={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByLabelText('Purchase amount (NT$)'), {
      target: { value: '000500' },
    });
    await user.click(screen.getByRole('button', { name: 'Add points' }));
    await vi.waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(api.to('POST', '/v1/staff/points/earn')[0]?.body).toEqual({
      scan_token: 'st1.scan-token',
      spend_amount: 500,
    });
  });

  it('keeps Add points disabled for zero however it is typed', async () => {
    const user = userEvent.setup();
    const api = mockApi({});
    render(
      <EarnForm
        {...earnProps}
        scan={scanResult()}
        isManager={false}
        onDone={vi.fn()}
        onScanAgain={vi.fn()}
      />,
    );
    const input = screen.getByLabelText('Purchase amount (NT$)');
    for (const zero of ['0', '00', '000000000']) {
      fireEvent.change(input, { target: { value: zero } });
      expect(screen.getByRole('button', { name: 'Add points' })).toBeDisabled();
    }
    fireEvent.change(input, { target: { value: '' } });
    await user.click(screen.getByRole('button', { name: '00' }));
    expect(input).toHaveValue('0');
    expect(screen.getByRole('button', { name: 'Add points' })).toBeDisabled();
    expect(api.calls).toHaveLength(0);
  });
});

describe('keypad length and maximum', () => {
  it('stops at nine digits from the keys and drops a two-digit key that does not fit', () => {
    expect(pressKey('12345678', '9')).toBe('123456789');
    expect(pressKey('123456789', '0')).toBe('123456789');
    expect(pressKey('12345678', '00')).toBe('12345678');
    expect(pressKey('1234567', '00')).toBe('123456700');
    expect(pressKey('123456789', 'back')).toBe('12345678');
  });

  it('keeps only digits from a pasted amount', () => {
    render(
      <EarnForm
        {...earnProps}
        scan={scanResult()}
        isManager={false}
        onDone={vi.fn()}
        onScanAgain={vi.fn()}
      />,
    );
    const input = screen.getByLabelText('Purchase amount (NT$)');
    fireEvent.change(input, { target: { value: 'NT$1,250' } });
    expect(input).toHaveValue('1250');
    fireEvent.change(input, { target: { value: '-35' } });
    expect(input).toHaveValue('35');
  });

  it('sends the spend at the API maximum unchanged', async () => {
    const user = userEvent.setup();
    const api = mockApi({ [EARN]: () => json(200, txn(1_000_000, 1_000_120)) });
    const onDone = vi.fn();
    render(
      <EarnForm
        {...earnProps}
        scan={scanResult()}
        isManager={false}
        onDone={onDone}
        onScanAgain={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByLabelText('Purchase amount (NT$)'), {
      target: { value: String(MAX_SPEND) },
    });
    await user.click(screen.getByRole('button', { name: 'Add points' }));
    await vi.waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(api.to('POST', '/v1/staff/points/earn')[0]?.body).toEqual({
      scan_token: 'st1.scan-token',
      spend_amount: MAX_SPEND,
    });
  });

  // A pasted ten-digit amount must not lose its last digit and become a
  // different, smaller amount, and nothing above the API maxima may be
  // submitted (security review Phase 3 follow-up).
  it('refuses amounts above the API maximum instead of sending or truncating them', async () => {
    const user = userEvent.setup();
    const api = mockApi({ [EARN]: () => json(200, txn(10, 130)) });
    render(
      <EarnForm
        {...earnProps}
        scan={scanResult()}
        isManager={true}
        onDone={vi.fn()}
        onScanAgain={vi.fn()}
      />,
    );
    const input = screen.getByLabelText('Purchase amount (NT$)');
    fireEvent.change(input, { target: { value: '1000000000' } });
    const truncated = (input as HTMLInputElement).value;
    fireEvent.change(input, { target: { value: String(MAX_SPEND + 1) } });
    const overSpend = !screen.getByRole('button', { name: 'Add points' }).hasAttribute('disabled');
    await user.click(screen.getByLabelText(/fixed number of points/));
    fireEvent.change(screen.getByLabelText('Points'), {
      target: { value: String(MAX_POINTS + 1) },
    });
    const overPoints = !screen.getByRole('button', { name: 'Add points' }).hasAttribute('disabled');
    expect(truncated).toBe('');
    expect(overSpend).toBe(false);
    expect(overPoints).toBe(false);
    expect(screen.getByText('At most 1,000,000. That amount was not entered.')).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });

  it('accepts exactly the maxima and refuses one more digit from the keypad', () => {
    mockApi({});
    render(
      <EarnForm
        {...earnProps}
        scan={scanResult()}
        isManager={true}
        onDone={vi.fn()}
        onScanAgain={vi.fn()}
      />,
    );
    const input = screen.getByLabelText('Purchase amount (NT$)');
    fireEvent.change(input, { target: { value: String(MAX_SPEND) } });
    expect((input as HTMLInputElement).value).toBe(String(MAX_SPEND));
    fireEvent.click(screen.getByRole('button', { name: '0' }));
    expect((input as HTMLInputElement).value).toBe(String(MAX_SPEND));
    expect(
      screen.getByText('At most 100,000,000. That amount was not entered.'),
    ).toBeInTheDocument();
  });
});

describe('redeem keypad', () => {
  it('redeems exactly the balance and shows a zero balance before confirming', async () => {
    const user = userEvent.setup();
    const api = mockApi({ [REDEEM]: () => json(200, txn(-120, 0)) });
    const onDone = vi.fn();
    render(
      <RedeemForm
        {...redeemProps}
        scan={scanResult(300, 120)}
        onDone={onDone}
        onScanAgain={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByLabelText('Points to redeem'), { target: { value: '0120' } });
    expect(screen.queryByText(/More than the balance/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Review' }));
    expect(screen.getByText('Redeem 120 points? The new balance will be 0.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Confirm' }));
    await vi.waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(api.to('POST', '/v1/staff/points/redeem')[0]?.body).toEqual({
      scan_token: 'st1.scan-token',
      points: 120,
    });
  });

  it('blocks one point over the balance and zero', () => {
    mockApi({});
    render(
      <RedeemForm
        {...redeemProps}
        scan={scanResult(300, 120)}
        onDone={vi.fn()}
        onScanAgain={vi.fn()}
      />,
    );
    const input = screen.getByLabelText('Points to redeem');
    fireEvent.change(input, { target: { value: '121' } });
    expect(screen.getByText('More than the balance of 120 points.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled();
    fireEvent.change(input, { target: { value: '000' } });
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled();
  });

  it('cannot redeem from a zero balance', () => {
    mockApi({});
    render(
      <RedeemForm
        {...redeemProps}
        scan={scanResult(300, 0)}
        onDone={vi.fn()}
        onScanAgain={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByLabelText('Points to redeem'), { target: { value: '1' } });
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled();
  });
});
