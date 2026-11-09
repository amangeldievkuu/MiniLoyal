import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { MemberDetail } from '../api/types';
import { ERASE_PHRASE, MemberDetailView } from '../pages/MemberDetailPage';
import { MembersList } from '../pages/MembersPage';
import { jsonResponse, renderWithProviders, stubFetch } from './providers';

function member(id: string, name: string, extra: Partial<MemberDetail> = {}): MemberDetail {
  return {
    id,
    app_id: 'a1',
    display_name: name,
    picture_url: null,
    language: 'zh-TW',
    is_friend: true,
    custom_fields: {},
    line_user_id: `U${id}`,
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-02T00:00:00Z',
    ...extra,
  };
}

describe('MembersList', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('lists members, loads more and searches', async () => {
    const { calls } = stubFetch((call) => {
      if (call.path !== '/v1/dashboard/apps/a1/members') return undefined;
      if (call.query.get('q') === 'Mei') {
        return jsonResponse(200, { items: [member('m1', 'Mei')], next_cursor: null });
      }
      if (call.query.get('cursor') === 'c2') {
        return jsonResponse(200, { items: [member('m3', 'Third')], next_cursor: null });
      }
      return jsonResponse(200, {
        items: [
          member('m1', 'Mei', {
            consent: { version: '2026-11', accepted_at: '2026-11-01T00:00:00Z' },
          }),
          member('m2', 'Kenji', { is_friend: false }),
        ],
        next_cursor: 'c2',
      });
    });
    const user = userEvent.setup();
    renderWithProviders(<MembersList appId="a1" />);

    expect(await screen.findByText('Mei')).toBeInTheDocument();
    expect(screen.getByText('Kenji')).toBeInTheDocument();
    expect(screen.getByText('2026-11')).toBeInTheDocument();
    expect(screen.getByText('Not a friend')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('Third')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();

    await user.type(screen.getByLabelText('Search'), 'Mei');
    await user.click(screen.getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(screen.queryByText('Kenji')).not.toBeInTheDocument());
    const searchCall = calls.find((c) => c.query.get('q') === 'Mei');
    expect(searchCall?.query.get('limit')).toBe('50');
  });
});

describe('MemberDetailView', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('erases only after the confirmation phrase is typed', async () => {
    const { calls } = stubFetch((call) => {
      if (call.method === 'GET' && call.path === '/v1/dashboard/members/m1') {
        return jsonResponse(200, member('m1', 'Mei', { phone: '0912345678' }));
      }
      if (call.method === 'DELETE' && call.path === '/v1/dashboard/members/m1') {
        return new Response(null, { status: 204 });
      }
      return undefined;
    });
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="owner" />);

    expect(await screen.findByText('Um1')).toBeInTheDocument();
    expect(screen.getByText('0912345678')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Erase personal data' }));
    const confirm = screen
      .getAllByRole('button', { name: 'Erase personal data', hidden: true })
      .at(-1);
    if (!confirm) throw new Error('confirmation button not rendered');
    expect(confirm).toBeDisabled();
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false);

    await user.type(screen.getByLabelText('Confirmation'), 'wrong');
    expect(confirm).toBeDisabled();
    await user.clear(screen.getByLabelText('Confirmation'));
    await user.type(screen.getByLabelText('Confirmation'), ERASE_PHRASE);
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    await waitFor(() =>
      expect(
        calls.some((c) => c.method === 'DELETE' && c.path === '/v1/dashboard/members/m1'),
      ).toBe(true),
    );
  });

  it('hides the privacy actions from staff', async () => {
    stubFetch((call) =>
      call.path === '/v1/dashboard/members/m1' ? jsonResponse(200, member('m1', 'Mei')) : undefined,
    );
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="staff" />);
    expect(await screen.findByText('Um1')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Erase personal data' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Export data' })).not.toBeInTheDocument();
  });

  it('downloads the export as a JSON file', async () => {
    const exportBody = {
      member: member('m1', 'Mei'),
      consents: [],
      exported_at: '2026-11-09T00:00:00Z',
    };
    stubFetch((call) => {
      if (call.path === '/v1/dashboard/members/m1') return jsonResponse(200, member('m1', 'Mei'));
      if (call.path === '/v1/dashboard/members/m1/export') return jsonResponse(200, exportBody);
      return undefined;
    });
    const createObjectURL = vi.fn<(blob: Blob) => string>(() => 'blob:mock');
    const revokeObjectURL = vi.fn();
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    const user = userEvent.setup();
    renderWithProviders(<MemberDetailView appId="a1" memberId="m1" role="admin" />);

    await user.click(await screen.findByRole('button', { name: 'Export data' }));

    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1));
    const blob = createObjectURL.mock.calls[0]?.[0];
    if (!blob) throw new Error('no blob was created');
    expect(blob.type).toBe('application/json');
    expect(JSON.parse(await blob.text())).toEqual(exportBody);
    expect(click).toHaveBeenCalledTimes(1);
    const anchor = click.mock.instances[0] as HTMLAnchorElement;
    expect(anchor.download).toBe('member-m1.json');
    click.mockRestore();
  });
});
