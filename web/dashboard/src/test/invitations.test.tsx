import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Invitation, TeamMember } from '../api/types';
import { AcceptInviteForm } from '../pages/AcceptInvitePage';
import { TeamView } from '../pages/TeamPage';
import { jsonResponse, renderWithProviders, stubFetch } from './providers';

const owner: TeamMember = {
  user_id: 'u1',
  email: 'owner@example.com',
  display_name: 'Owner',
  role: 'owner',
  created_at: '2026-01-01T00:00:00Z',
};

const invitation: Invitation = {
  id: 'inv1',
  email: 'new@example.com',
  role: 'admin',
  expires_at: '2026-11-16T00:00:00Z',
  created_at: '2026-11-09T00:00:00Z',
};

describe('TeamView invitations', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('creates an invitation, shows the link once and refreshes the pending list', async () => {
    let pending: Invitation[] = [];
    const { calls } = stubFetch((call) => {
      if (call.path === '/v1/dashboard/tenants/t1/team') return jsonResponse(200, [owner]);
      if (call.method === 'GET' && call.path === '/v1/dashboard/tenants/t1/invitations') {
        return jsonResponse(200, pending);
      }
      if (call.method === 'POST' && call.path === '/v1/dashboard/tenants/t1/invitations') {
        pending = [invitation];
        return jsonResponse(201, {
          ...invitation,
          invite_url: 'https://dashboard.example/accept-invite?token=abc123',
        });
      }
      return undefined;
    });
    const user = userEvent.setup();
    renderWithProviders(<TeamView tenantId="t1" role="owner" meUserId="u1" />);

    expect(await screen.findByText('No pending invitations.')).toBeInTheDocument();
    // The deprecated add-by-email form is gone.
    expect(screen.queryByRole('button', { name: 'Add member' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Invite' }));
    await user.type(screen.getByLabelText('Email'), 'new@example.com');
    await user.selectOptions(screen.getByLabelText('Role'), 'admin');
    const submit = screen.getAllByRole('button', { name: 'Invite', hidden: true }).at(-1);
    if (!submit) throw new Error('invite submit button not rendered');
    await user.click(submit);

    expect(
      await screen.findByText('https://dashboard.example/accept-invite?token=abc123'),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.body).toEqual({ email: 'new@example.com', role: 'admin' });
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy();

    await waitFor(() => expect(screen.getByText('new@example.com')).toBeInTheDocument());
    expect(screen.queryByText('No pending invitations.')).not.toBeInTheDocument();
  });

  it('lets an admin grant admin or staff only', async () => {
    stubFetch((call) => {
      if (call.path === '/v1/dashboard/tenants/t1/team') return jsonResponse(200, [owner]);
      if (call.path === '/v1/dashboard/tenants/t1/invitations') return jsonResponse(200, []);
      return undefined;
    });
    const user = userEvent.setup();
    renderWithProviders(<TeamView tenantId="t1" role="admin" meUserId="u2" />);
    await user.click(await screen.findByRole('button', { name: 'Invite' }));
    const options = screen
      .getAllByRole('option', { hidden: true })
      .map((o) => (o as HTMLOptionElement).value);
    expect(options).toEqual(['admin', 'staff']);
  });
});

describe('AcceptInviteForm', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('submits the token with the password and signs in', async () => {
    const session = {
      access_token: 'tok',
      token_type: 'Bearer',
      expires_in: 900,
      user: { id: 'u9', email: 'new@example.com', totp_enabled: false, created_at: '' },
    };
    const { calls } = stubFetch((call) =>
      call.path === '/v1/dashboard/auth/accept-invite' ? jsonResponse(200, session) : undefined,
    );
    const onAccepted = vi.fn();
    const user = userEvent.setup();
    render(<AcceptInviteForm token="tok-1" onAccepted={onAccepted} />);

    await user.type(screen.getByLabelText('Password'), 'correct horse battery');
    await user.click(screen.getByRole('button', { name: 'Accept invitation' }));

    await waitFor(() => expect(onAccepted).toHaveBeenCalledWith(session));
    expect(calls[0]?.body).toEqual({ token: 'tok-1', password: 'correct horse battery' });
  });

  it('shows the field error when the account needs a password', async () => {
    stubFetch(() =>
      jsonResponse(400, {
        code: 'validation_failed',
        message: 'request is invalid',
        request_id: 'r1',
        details: { password: 'required for a new account' },
      }),
    );
    const user = userEvent.setup();
    render(<AcceptInviteForm token="tok-1" onAccepted={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Accept invitation' }));
    expect(await screen.findByText('required for a new account')).toBeInTheDocument();
  });
});
