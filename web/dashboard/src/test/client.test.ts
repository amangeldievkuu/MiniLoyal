import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  ApiError,
  api,
  createIdempotencyKeyHolder,
  onSessionLost,
  tokenStore,
  unwrap,
} from '../api/client';

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const session = {
  access_token: 't2',
  token_type: 'Bearer',
  expires_in: 900,
  user: { id: 'u1', email: 'a@b.c', totp_enabled: false, created_at: '2026-01-01T00:00:00Z' },
};

function mockFetch(handler: (req: Request) => Response | Promise<Response>) {
  const calls: Request[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(input, init);
    calls.push(req);
    return handler(req);
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

const pathOf = (req: Request) => new URL(req.url).pathname;

describe('authFetch', () => {
  beforeEach(() => tokenStore.set('t1'));
  afterEach(() => {
    vi.unstubAllGlobals();
    tokenStore.clear();
  });

  it('refreshes once and retries a protected call after a 401', async () => {
    const calls = mockFetch((req) => {
      switch (pathOf(req)) {
        case '/v1/dashboard/tenants':
          return req.headers.get('Authorization') === 'Bearer t2'
            ? json(200, [])
            : json(401, { code: 'unauthorized', message: 'expired', request_id: 'r1' });
        case '/v1/dashboard/auth/refresh':
          expect(req.method).toBe('POST');
          return json(200, session);
        default:
          return json(404, { code: 'not_found', message: 'nope', request_id: 'r0' });
      }
    });

    const data = unwrap(await api.GET('/v1/dashboard/tenants'));

    expect(data).toEqual([]);
    expect(calls.map(pathOf)).toEqual([
      '/v1/dashboard/tenants',
      '/v1/dashboard/auth/refresh',
      '/v1/dashboard/tenants',
    ]);
    expect(calls[0]?.headers.get('Authorization')).toBe('Bearer t1');
    expect(calls[2]?.headers.get('Authorization')).toBe('Bearer t2');
    expect(tokenStore.get()).toBe('t2');
  });

  it('clears the session and notifies when the refresh fails', async () => {
    const lost = vi.fn();
    const unsubscribe = onSessionLost(lost);
    const calls = mockFetch((req) =>
      pathOf(req) === '/v1/dashboard/auth/refresh'
        ? json(401, { code: 'unauthorized', message: 'no cookie', request_id: 'r2' })
        : json(401, { code: 'unauthorized', message: 'expired', request_id: 'r3' }),
    );

    await expect(async () => unwrap(await api.GET('/v1/dashboard/me'))).rejects.toMatchObject({
      status: 401,
      code: 'unauthorized',
      requestId: 'r3',
    });
    expect(calls.map(pathOf)).toEqual(['/v1/dashboard/me', '/v1/dashboard/auth/refresh']);
    expect(tokenStore.get()).toBeNull();
    expect(lost).toHaveBeenCalledTimes(1);
    unsubscribe();
  });

  it('does not refresh for auth routes', async () => {
    const calls = mockFetch(() =>
      json(401, { code: 'unauthorized', message: 'wrong password', request_id: 'r4' }),
    );
    const result = await api.POST('/v1/dashboard/auth/login', {
      body: { email: 'a@b.c', password: 'nope' },
    });
    expect(() => unwrap(result)).toThrow(ApiError);
    expect(calls.map(pathOf)).toEqual(['/v1/dashboard/auth/login']);
  });

  it('exposes field details from the error envelope', () => {
    const err = new ApiError(
      400,
      {
        code: 'validation_failed',
        message: 'invalid',
        request_id: 'r5',
        details: { email: 'bad' },
      },
      'fallback',
    );
    expect(err.field('email')).toBe('bad');
    expect(err.field('password')).toBeUndefined();
  });
});

describe('idempotency key holder', () => {
  it('keeps the key until reset, then mints a new one', () => {
    const holder = createIdempotencyKeyHolder();
    const first = holder.key();
    expect(first).toMatch(/^[0-9a-f-]{36}$/);
    expect(holder.key()).toBe(first);
    holder.reset();
    expect(holder.key()).not.toBe(first);
  });
});
