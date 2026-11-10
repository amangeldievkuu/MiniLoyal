import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { PointsRules } from '../api/types';
import { PointsRulesView } from '../pages/PointsRulesPage';
import { jsonResponse, renderWithProviders, stubFetch, type RecordedCall } from './providers';

afterEach(() => vi.unstubAllGlobals());

const rules = (extra: Partial<PointsRules> = {}): PointsRules => ({
  currency: 'TWD',
  spend_per_point: 100,
  points_per_unit: 1,
  min_spend: 0,
  max_points_per_txn: null,
  expiry_days: null,
  enabled: true,
  updated_at: '2026-11-01T00:00:00Z',
  ...extra,
});

async function renderRules(onPut?: (call: RecordedCall) => Response) {
  const stub = stubFetch((call) => {
    if (call.path !== '/v1/dashboard/apps/a1/points-rules') return undefined;
    if (call.method === 'GET') return jsonResponse(200, rules());
    if (call.method === 'PUT') {
      return (
        onPut?.(call) ??
        jsonResponse(200, { ...(call.body as object), updated_at: '2026-11-10T00:00:00Z' })
      );
    }
    return undefined;
  });
  renderWithProviders(<PointsRulesView appId="a1" role="owner" />);
  const preview = await screen.findByTestId('rules-preview');
  return { preview, calls: stub.calls };
}

const set = (label: string, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });

describe('rules preview never rounds what it shows', () => {
  // The rule is whole currency units and whole points: a value the API
  // would refuse, or one the form would have to round, shows no preview
  // and cannot be saved, rather than previewing a rounded rule.
  it.each([
    ['a decimal spend', 'Spend (NT$)', '99.5'],
    ['a decimal comma', 'Spend (NT$)', '99,5'],
    ['an exponent', 'Spend (NT$)', '1e2'],
    ['a negative spend', 'Spend (NT$)', '-100'],
    ['zero spend', 'Spend (NT$)', '0'],
    ['decimal points', 'Points earned', '1.5'],
    ['zero points', 'Points earned', '0'],
    ['a decimal cap', 'Most points per purchase', '2.5'],
    ['a zero cap', 'Most points per purchase', '0'],
    ['a decimal expiry', 'Points expire after (days)', '0.5'],
    ['a decimal minimum', 'Minimum spend (NT$)', '30.5'],
  ])('%s hides the preview and disables Save', async (_name, label, value) => {
    const { preview, calls } = await renderRules();
    set(label, value);
    expect(preview).toBeEmptyDOMElement();
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save).toBeDisabled();
    fireEvent.submit(save.closest('form') as HTMLFormElement);
    expect(calls.some((c) => c.method === 'PUT')).toBe(false);
  });

  it('reads leading zeros and spaces as the plain number and saves exactly what it shows', async () => {
    const { preview, calls } = await renderRules();
    const user = userEvent.setup();
    set('Spend (NT$)', ' 0050 ');
    set('Points earned', '007');
    set('Minimum spend (NT$)', '000');
    set('Most points per purchase', '01');
    set('Points expire after (days)', '1');
    expect(preview).toHaveTextContent('Spend NT$50, earn 7 points; points expire after 1 day');
    expect(preview).toHaveTextContent('At most 1 point per purchase.');
    expect(preview).not.toHaveTextContent('Purchases under');

    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true));
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      currency: 'TWD',
      spend_per_point: 50,
      points_per_unit: 7,
      min_spend: 0,
      max_points_per_txn: 1,
      expiry_days: 1,
      enabled: true,
    });
  });

  it('uses the singular for one point and the plural otherwise', async () => {
    const { preview } = await renderRules();
    set('Points earned', '1');
    expect(preview).toHaveTextContent('Spend NT$100, earn 1 point; points never expire');
    set('Points earned', '2');
    expect(preview).toHaveTextContent('Spend NT$100, earn 2 points;');
  });

  it('previews the largest values the API accepts without changing them', async () => {
    const { preview } = await renderRules();
    set('Spend (NT$)', '1000000');
    set('Points earned', '1000');
    set('Most points per purchase', '1000000');
    set('Points expire after (days)', '3650');
    set('Minimum spend (NT$)', '10000000');
    expect(preview).toHaveTextContent('1000000');
    expect(preview).toHaveTextContent('earn 1000 points');
    expect(preview).toHaveTextContent('3650 days');
    expect(preview).toHaveTextContent('10000000');
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
  });

  it('shows the API field error for a value above its maximum', async () => {
    const { calls } = await renderRules(() =>
      jsonResponse(400, {
        code: 'validation_failed',
        message: 'request is invalid',
        request_id: 'r1',
        details: { points_per_unit: 'must be 1 to 1000' },
      }),
    );
    const user = userEvent.setup();
    set('Points earned', '1001');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('must be 1 to 1000')).toBeInTheDocument();
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ points_per_unit: 1001 });
  });
});
