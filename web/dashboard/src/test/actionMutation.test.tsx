import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { useActionMutation } from '../api/actionMutation';

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

interface Vars {
  coupon: string;
  reason: string;
}

// The server answers one key with two payloads with 422, so the key must
// follow the body: a retry of one body reuses its key, another body never
// does, and a success frees the body for a new action.
describe('useActionMutation', () => {
  function setup(fail: (v: Vars) => boolean) {
    const calls: { vars: Vars; key: string }[] = [];
    const onSuccess = vi.fn();
    const run = vi
      .fn<(vars: Vars, key: string) => Promise<string>>()
      .mockImplementation((vars, key) => {
        calls.push({ vars, key });
        return fail(vars) ? Promise.reject(new Error('network')) : Promise.resolve(vars.coupon);
      });
    const hook = renderHook(() => useActionMutation(run, { onSuccess }), { wrapper });
    const send = async (v: Vars) => {
      await act(async () => {
        await hook.result.current.mutateAsync(v).catch(() => undefined);
      });
    };
    return { calls, onSuccess, send };
  }

  it('reuses the key when the same body is retried after a failure', async () => {
    let failing = true;
    const { calls, send, onSuccess } = setup(() => failing);
    const body = { coupon: 'c1', reason: 'issued twice' };
    await send(body);
    failing = false;
    await send({ ...body });
    expect(calls).toHaveLength(2);
    expect(calls[1]?.key).toBe(calls[0]?.key);
    expect(onSuccess).toHaveBeenCalledTimes(1);
  });

  it('never sends one key with two bodies', async () => {
    const { calls, send } = setup(() => true);
    await send({ coupon: 'c1', reason: 'issued twice' });
    await send({ coupon: 'c1', reason: 'issued by mistake' });
    await send({ coupon: 'c2', reason: 'issued twice' });
    await send({ coupon: 'c1', reason: 'issued twice' });
    const byKey = new Map<string, string>();
    for (const c of calls) {
      const body = JSON.stringify(c.vars);
      const seen = byKey.get(c.key);
      expect(seen === undefined || seen === body).toBe(true);
      byKey.set(c.key, body);
    }
    expect(new Set(calls.map((c) => c.key)).size).toBe(3);
    expect(calls[3]?.key).toBe(calls[0]?.key);
  });

  it('gives the same body a fresh key after a success', async () => {
    const { calls, send } = setup(() => false);
    const body = { coupon: 'c1', reason: 'goodwill' };
    await send(body);
    await send(body);
    expect(calls).toHaveLength(2);
    expect(calls[1]?.key).not.toBe(calls[0]?.key);
  });
});
