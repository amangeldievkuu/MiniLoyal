import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { MemberSession } from '../src/index';
import { MiniLoyal, MiniLoyalError } from '../src/index';

// Secrets the SDK handles during sign-in. None of them may surface in a
// thrown error, however the exchange fails.
const ID_TOKEN = 'eyJhbGciOiJFUzI1NiJ9.secret-id-token-payload.signature';
const LIFF_ACCESS = 'liff-access-token-secret';

const liffMock = vi.hoisted(() => ({
  init: vi.fn(async () => {}),
  isLoggedIn: vi.fn(() => true),
  login: vi.fn(),
  logout: vi.fn(),
  getIDToken: vi.fn((): string | null => 'eyJhbGciOiJFUzI1NiJ9.secret-id-token-payload.signature'),
  getAccessToken: vi.fn((): string | null => 'liff-access-token-secret'),
  getLanguage: vi.fn(() => 'zh-TW'),
  getFriendship: vi.fn(async () => ({ friendFlag: true })),
}));

vi.mock('@line/liff', () => ({ default: liffMock }));

const session: MemberSession = {
  session_token: 'session-1',
  token_type: 'Bearer',
  expires_in: 3600,
  member: {
    id: 'f7c1d2e3-0000-4000-8000-000000000001',
    app_id: 'f7c1d2e3-0000-4000-8000-000000000000',
    display_name: 'Mei',
    is_friend: true,
    custom_fields: {},
    created_at: '2026-11-09T12:00:00Z',
  },
  app: {
    id: 'f7c1d2e3-0000-4000-8000-000000000000',
    name: 'Demo',
    locale: 'zh-TW',
    oa_basic_id: null,
  },
  is_new: false,
};

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function scriptedFetch(responses: (Response | (() => Response))[]) {
  const fetch = vi.fn(async () => {
    const next = responses.shift();
    if (!next) throw new Error('unexpected fetch');
    return typeof next === 'function' ? next() : next;
  });
  return fetch as unknown as typeof globalThis.fetch;
}

const options = {
  appId: 'f7c1d2e3-0000-4000-8000-000000000000',
  liffId: '1661234567-AbCdEfGh',
  baseUrl: 'https://api.test',
};

/** Everything an application might log about an error. */
function surface(err: unknown): string {
  const e = err as MiniLoyalError;
  return [
    String(err),
    e.message,
    e.code,
    e.stack ?? '',
    JSON.stringify(e),
    JSON.stringify(e.details ?? null),
    JSON.stringify(
      Object.getOwnPropertyNames(e).map((k) => (e as unknown as Record<string, unknown>)[k]),
    ),
  ].join('\n');
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-11-09T12:00:00Z'));
  liffMock.isLoggedIn.mockReturnValue(true);
  liffMock.getIDToken.mockReturnValue(ID_TOKEN);
  liffMock.getAccessToken.mockReturnValue(LIFF_ACCESS);
});

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe('rate limiting', () => {
  it('surfaces a 429 envelope as MiniLoyalError with retry_after_seconds in details', async () => {
    const fetch = scriptedFetch([
      json(200, session),
      json(
        429,
        {
          code: 'rate_limited',
          message: 'too many requests',
          request_id: 'r-429',
          details: { retry_after_seconds: 7 },
        },
        { 'Retry-After': '7' },
      ),
    ]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    const err = await ml.me().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MiniLoyalError);
    const e = err as MiniLoyalError;
    expect(e.code).toBe('rate_limited');
    expect(e.status).toBe(429);
    expect(e.requestId).toBe('r-429');
    expect(e.details?.retry_after_seconds).toBe(7);
    // field() is for string validation messages; a number is not one.
    expect(e.field('retry_after_seconds')).toBeUndefined();
    // A 429 is not a sign-out.
    expect(ml.state).toBe('signed_in');
  });

  it('keeps the status and request id when a 429 arrives without an envelope', async () => {
    const fetch = scriptedFetch([
      json(200, session),
      new Response('Too Many Requests', {
        status: 429,
        headers: { 'Retry-After': '30', 'X-Request-ID': 'edge-429' },
      }),
    ]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    const err = (await ml.me().catch((e: unknown) => e)) as MiniLoyalError;
    expect(err.code).toBe('http_429');
    expect(err.status).toBe(429);
    expect(err.requestId).toBe('edge-429');
    // Documented: the SDK does not read Retry-After; callers who need it
    // without an envelope must use the response headers themselves.
    expect(err.details).toBeUndefined();
  });

  it('surfaces a 429 on the sign-in exchange itself without signing in', async () => {
    const fetch = scriptedFetch([
      json(429, {
        code: 'rate_limited',
        message: 'too many requests',
        request_id: 'r-login',
        details: { retry_after_seconds: 1 },
      }),
    ]);
    const err = (await MiniLoyal.init({ ...options, fetch }).catch(
      (e: unknown) => e,
    )) as MiniLoyalError;
    expect(err.code).toBe('rate_limited');
    expect(err.details?.retry_after_seconds).toBe(1);
  });
});

describe('init never leaks the LIFF tokens', () => {
  const failures: Record<string, () => Response> = {
    '401 envelope': () =>
      json(401, {
        code: 'unauthorized',
        message: 'invalid LINE ID token',
        request_id: 'r-401',
      }),
    '404 envelope with details': () =>
      json(404, {
        code: 'not_found',
        message: 'app not found',
        request_id: 'r-404',
        details: { app_id: options.appId },
      }),
    '500 without a body': () => new Response(null, { status: 500 }),
    '502 html': () => new Response('<html>bad gateway</html>', { status: 502 }),
    '200 that is not a session': () => json(200, { unexpected: true }),
    '200 with a non-JSON body': () =>
      new Response('ok', { status: 200, headers: { 'Content-Type': 'text/plain' } }),
    'network failure': () => {
      throw new TypeError('Failed to fetch');
    },
  };
  for (const [name, respond] of Object.entries(failures)) {
    it(`on ${name}`, async () => {
      const changes: unknown[] = [];
      const fetch = scriptedFetch([respond]);
      const err = await MiniLoyal.init({
        ...options,
        fetch,
        onSessionChange: (s) => changes.push(s),
      }).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(MiniLoyalError);
      const text = surface(err);
      expect(text).not.toContain(ID_TOKEN);
      expect(text).not.toContain('secret-id-token-payload');
      expect(text).not.toContain(LIFF_ACCESS);
      expect(changes).toHaveLength(0);
    });
  }

  it('does not echo the tokens when the network error message quotes the request', async () => {
    // Some fetch polyfills include request details in their errors; the
    // SDK must not add the tokens on its own, and must pass nothing but the
    // message through.
    const fetch = scriptedFetch([
      () => {
        throw new TypeError('request to https://api.test/v1/auth/line failed');
      },
    ]);
    const err = (await MiniLoyal.init({ ...options, fetch }).catch(
      (e: unknown) => e,
    )) as MiniLoyalError;
    expect(err.code).toBe('network');
    expect(surface(err)).not.toContain(ID_TOKEN);
    expect(surface(err)).not.toContain(LIFF_ACCESS);
  });

  it('does not keep the tokens on the client after a failed exchange', async () => {
    const fetch = scriptedFetch([
      json(401, { code: 'unauthorized', message: 'nope', request_id: 'r' }),
    ]);
    const err = await MiniLoyal.init({ ...options, fetch }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MiniLoyalError);
    // A later successful sign-in with a different token must not reuse the
    // failed one. LIFF reports a login, so init exchanges straight away.
    const ok = scriptedFetch([json(200, session)]);
    liffMock.getIDToken.mockReturnValue('second-id-token');
    const ml = await MiniLoyal.init({ ...options, fetch: ok });
    const body = JSON.parse(
      String((ok as unknown as ReturnType<typeof vi.fn>).mock.calls[0]?.[1]?.body),
    ) as { id_token: string };
    expect(body.id_token).toBe('second-id-token');
    expect(JSON.stringify(ml)).not.toContain(ID_TOKEN);
  });
});
