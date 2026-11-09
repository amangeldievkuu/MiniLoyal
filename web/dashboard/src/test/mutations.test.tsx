import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { useIdempotentMutation } from '../api/mutations';

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe('useIdempotentMutation', () => {
  it('reuses the Idempotency-Key on retry and rotates it after success', async () => {
    const keys: string[] = [];
    const run = vi
      .fn<(vars: string, key: string) => Promise<string>>()
      .mockImplementation((vars, key) => {
        keys.push(key);
        return vars === 'fail' ? Promise.reject(new Error('boom')) : Promise.resolve(vars);
      });
    const { result } = renderHook(() => useIdempotentMutation(run), { wrapper });

    await act(async () => {
      await result.current.mutateAsync('fail').catch(() => undefined);
    });
    await act(async () => {
      await result.current.mutateAsync('ok');
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(async () => {
      await result.current.mutateAsync('ok');
    });

    expect(keys).toHaveLength(3);
    expect(keys[0]).toBe(keys[1]);
    expect(keys[2]).not.toBe(keys[1]);
  });
});
