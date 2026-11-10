import { act, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { MemberPanel } from '../components/MemberPanel';
import { pressKey } from '../components/Keypad';
import { normalizeInviteCode } from '../auth/session';
import { json, mockApi, scanResult, staffSession } from '../test/api';
import { Counter } from './Counter';

describe('scan token countdown', () => {
  it('calls onExpire when the scan token runs out', () => {
    vi.useFakeTimers();
    const onExpire = vi.fn();
    const scan = scanResult(3);
    render(
      <MemberPanel
        member={scan.member}
        balance={scan.balance}
        expiresAt={scan.expires_at}
        onExpire={onExpire}
      />,
    );
    expect(screen.getByText('Scan again in 0:03')).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(2000));
    expect(screen.getByText('Scan again in 0:01')).toBeInTheDocument();
    expect(onExpire).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1000));
    expect(onExpire).toHaveBeenCalledOnce();
  });

  it('clears the member panel and asks for a new scan when the token expires', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mockApi({ 'POST /v1/staff/scan': () => json(200, scanResult(300)) });
    render(<Counter session={staffSession()} />);

    fireEvent.click(screen.getByText('Type the code'));
    fireEvent.change(screen.getByLabelText('Card code'), { target: { value: 'ml1.code' } });
    fireEvent.click(screen.getByRole('button', { name: 'Look up' }));
    expect(await screen.findByText('Lin')).toBeInTheDocument();
    // The panel rendered after a fetch, outside act: let React run its
    // effects (which start the countdown) before moving fake time.
    await act(async () => {});
    expect(screen.getByText(/Scan again in [45]:/)).toBeInTheDocument();

    act(() => vi.advanceTimersByTime(301_000));
    expect(screen.queryByText('Lin')).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('This scan has timed out');
    expect(screen.getByRole('button', { name: 'Scan member card' })).toBeInTheDocument();
  });
});

describe('helpers', () => {
  it('keypad keys edit the digits', () => {
    expect(pressKey('', '1')).toBe('1');
    expect(pressKey('0', '5')).toBe('5');
    expect(pressKey('12', '00')).toBe('1200');
    expect(pressKey('123', 'back')).toBe('12');
    expect(pressKey('123', 'clear')).toBe('');
    expect(pressKey('123456789', '1')).toBe('123456789');
    expect(pressKey('', '00')).toBe('0');
  });

  it('normalizes invite codes the way the API compares them', () => {
    expect(normalizeInviteCode(' abcd-efgh jkmn ')).toBe('ABCDEFGHJKMN');
  });
});
