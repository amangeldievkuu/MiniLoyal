import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { SecurityActivityCard } from '../pages/SecurityActivityCard';
import { jsonResponse, renderWithProviders, stubFetch } from './providers';

describe('SecurityActivityCard', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('lists events with translated names and loads more', async () => {
    const { calls } = stubFetch((call) => {
      if (call.path !== '/v1/dashboard/me/security-events') return undefined;
      if (call.query.get('cursor') === 'next') {
        return jsonResponse(200, {
          items: [
            {
              id: 1,
              event: 'account.created',
              ip: '203.0.113.1',
              user_agent: null,
              created_at: '2026-11-01T00:00:00Z',
            },
          ],
          next_cursor: null,
        });
      }
      return jsonResponse(200, {
        items: [
          {
            id: 3,
            event: 'login.success',
            ip: '203.0.113.9',
            user_agent: 'Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/142 Safari/537.36',
            created_at: '2026-11-09T08:00:00Z',
          },
          {
            id: 2,
            event: 'something.new',
            ip: null,
            user_agent: null,
            created_at: '2026-11-08T08:00:00Z',
          },
        ],
        next_cursor: 'next',
      });
    });
    const user = userEvent.setup();
    renderWithProviders(<SecurityActivityCard />);

    expect(await screen.findByText('Signed in')).toBeInTheDocument();
    expect(screen.getByText('203.0.113.9')).toBeInTheDocument();
    // Unknown events fall back to their raw name; long user agents are truncated.
    expect(screen.getByText('something.new')).toBeInTheDocument();
    expect(screen.getByText(/^Mozilla\/5\.0 \(Macintosh\).*…$/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('Account created')).toBeInTheDocument();
    expect(calls.map((c) => c.query.get('cursor'))).toEqual([null, 'next']);
  });

  it('shows an empty state', async () => {
    stubFetch(() => jsonResponse(200, { items: [], next_cursor: null }));
    renderWithProviders(<SecurityActivityCard />);
    expect(await screen.findByText('No activity yet.')).toBeInTheDocument();
  });
});
