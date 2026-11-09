import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { LoginForm } from '../pages/auth/LoginForm';

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const session = {
  access_token: 'tok',
  token_type: 'Bearer',
  expires_in: 900,
  user: {
    id: 'u1',
    email: 'owner@example.com',
    totp_enabled: true,
    created_at: '2026-01-01T00:00:00Z',
  },
};

describe('LoginForm', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('shows the MFA step when the server returns a challenge, then signs in', async () => {
    const bodies: unknown[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req = input instanceof Request ? input : new Request(input, init);
        const path = new URL(req.url).pathname;
        bodies.push(await req.json());
        if (path === '/v1/dashboard/auth/login') {
          return json(200, { mfa: { mfa_token: 'mfa-1', expires_in: 300 } });
        }
        if (path === '/v1/dashboard/auth/mfa') return json(200, session);
        return json(404, {});
      }),
    );
    const onLoggedIn = vi.fn();
    const user = userEvent.setup();
    render(<LoginForm onLoggedIn={onLoggedIn} />);

    await user.type(screen.getByLabelText('Email'), 'owner@example.com');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    const code = await screen.findByLabelText('Code');
    expect(screen.getByRole('heading', { name: 'Two-step verification' })).toBeInTheDocument();
    await user.type(code, '123456');
    await user.click(screen.getByRole('button', { name: 'Verify' }));

    expect(onLoggedIn).toHaveBeenCalledWith(session);
    expect(bodies[1]).toEqual({ mfa_token: 'mfa-1', code: '123456' });
  });

  it('shows the server message on wrong credentials', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        json(401, { code: 'unauthorized', message: 'Wrong email or password', request_id: 'r9' }),
      ),
    );
    const user = userEvent.setup();
    render(<LoginForm onLoggedIn={vi.fn()} />);
    await user.type(screen.getByLabelText('Email'), 'owner@example.com');
    await user.type(screen.getByLabelText('Password'), 'nope nope nope');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Wrong email or password');
    expect(screen.getByText(/r9/)).toBeInTheDocument();
  });
});
