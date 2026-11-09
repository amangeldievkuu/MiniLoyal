import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Member, MemberSession } from '../src/index';
import { MiniLoyal, MiniLoyalError } from '../src/index';

const liffMock = vi.hoisted(() => ({
  init: vi.fn(async () => {}),
  isLoggedIn: vi.fn(() => true),
  login: vi.fn(),
  logout: vi.fn(),
  getIDToken: vi.fn((): string | null => 'id-token-1'),
  getAccessToken: vi.fn((): string | null => 'liff-access-1'),
  getLanguage: vi.fn(() => 'zh-TW'),
  getFriendship: vi.fn(async () => ({ friendFlag: true })),
}));

vi.mock('@line/liff', () => ({ default: liffMock }));

const member: Member = {
  id: 'f7c1d2e3-0000-4000-8000-000000000001',
  app_id: 'f7c1d2e3-0000-4000-8000-000000000000',
  display_name: 'Mei',
  picture_url: 'https://profile.line-scdn.net/x',
  is_friend: true,
  custom_fields: {},
  created_at: '2026-11-09T12:00:00Z',
};

function sessionBody(overrides: Partial<MemberSession> = {}): MemberSession {
  return {
    session_token: 'session-1',
    token_type: 'Bearer',
    expires_in: 3600,
    member,
    app: { id: member.app_id, name: 'Demo Cafe', locale: 'zh-TW', oa_basic_id: '@demo' },
    is_new: false,
    ...overrides,
  };
}

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

type Call = { url: string; init: RequestInit };

/** A scripted fetch: each call takes the next queued response. */
function scriptedFetch(responses: (Response | (() => Response))[]) {
  const calls: Call[] = [];
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    const next = responses.shift();
    if (!next) throw new Error(`unexpected fetch ${String(input)}`);
    return typeof next === 'function' ? next() : next;
  });
  return { fetch: fetch as unknown as typeof globalThis.fetch, calls };
}

function header(calls: Call[], index: number, name: string): string | undefined {
  const call = calls[index];
  if (!call) throw new Error(`no call at index ${index}`);
  const h = call.init.headers as Record<string, string> | undefined;
  return h?.[name];
}

const options = {
  appId: member.app_id,
  liffId: '1661234567-AbCdEfGh',
  baseUrl: 'https://api.test/',
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-11-09T12:00:00Z'));
  liffMock.isLoggedIn.mockReturnValue(true);
  liffMock.getIDToken.mockReturnValue('id-token-1');
  liffMock.getAccessToken.mockReturnValue('liff-access-1');
});

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe('MiniLoyal.init', () => {
  it('rejects missing options', async () => {
    await expect(MiniLoyal.init({ ...options, appId: '' })).rejects.toThrow(/appId/);
    await expect(MiniLoyal.init({ ...options, liffId: '' })).rejects.toThrow(/liffId/);
  });

  it('initialises LIFF and exchanges the ID token for a session', async () => {
    const { fetch, calls } = scriptedFetch([json(200, sessionBody({ is_new: true }))]);
    const changes: unknown[] = [];
    const ml = await MiniLoyal.init({ ...options, fetch, onSessionChange: (s) => changes.push(s) });

    expect(liffMock.init).toHaveBeenCalledWith({ liffId: options.liffId });
    expect(ml.state).toBe('signed_in');
    expect(ml.baseUrl).toBe('https://api.test');
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toBe('https://api.test/v1/auth/line');
    expect(calls[0]?.init.method).toBe('POST');
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({
      app_id: member.app_id,
      id_token: 'id-token-1',
      liff_access_token: 'liff-access-1',
      language: 'zh-TW',
    });
    expect(header(calls, 0, 'Authorization')).toBeUndefined();
    expect(ml.session?.token).toBe('session-1');
    expect(ml.session?.isNew).toBe(true);
    expect(ml.session?.app.name).toBe('Demo Cafe');
    expect(changes).toHaveLength(1);
  });

  it('sends the member to LINE login and reports redirecting without throwing', async () => {
    liffMock.isLoggedIn.mockReturnValue(false);
    const { fetch, calls } = scriptedFetch([]);
    const ml = await MiniLoyal.init({ ...options, fetch, redirectUri: 'https://app.test/back' });

    expect(liffMock.login).toHaveBeenCalledWith({ redirectUri: 'https://app.test/back' });
    expect(ml.state).toBe('redirecting');
    expect(ml.session).toBeNull();
    expect(calls).toHaveLength(0);
    await expect(ml.me()).rejects.toMatchObject({ code: 'redirecting' });
  });

  it('stays signed out with autoLogin false until login() is called', async () => {
    liffMock.isLoggedIn.mockReturnValue(false);
    const { fetch, calls } = scriptedFetch([json(200, sessionBody())]);
    const ml = await MiniLoyal.init({ ...options, fetch, autoLogin: false });
    expect(ml.state).toBe('signed_out');
    expect(liffMock.login).not.toHaveBeenCalled();

    liffMock.isLoggedIn.mockReturnValue(true);
    await ml.login();
    expect(ml.state).toBe('signed_in');
    expect(calls).toHaveLength(1);
  });
});

describe('session refresh', () => {
  it('re-exchanges before a call when the session is about to expire', async () => {
    const { fetch, calls } = scriptedFetch([
      json(200, sessionBody({ expires_in: 3600 })),
      json(200, sessionBody({ session_token: 'session-2' })),
      json(200, member),
    ]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    vi.setSystemTime(new Date('2026-11-09T12:59:30Z')); // 30 s left, under the 60 s margin
    liffMock.getIDToken.mockReturnValue('id-token-2');

    await ml.me();
    expect(calls.map((c) => c.url)).toEqual([
      'https://api.test/v1/auth/line',
      'https://api.test/v1/auth/line',
      'https://api.test/v1/me',
    ]);
    expect(JSON.parse(String(calls[1]?.init.body)).id_token).toBe('id-token-2');
    expect(header(calls, 2, 'Authorization')).toBe('Bearer session-2');
  });

  it('retries once after a 401 with a fresh session, then gives up and clears the session', async () => {
    const { fetch, calls } = scriptedFetch([
      json(200, sessionBody()),
      json(401, { code: 'unauthorized', message: 'expired', request_id: 'r1' }),
      json(200, sessionBody({ session_token: 'session-2' })),
      json(200, { ...member, display_name: 'Mei 2' }),
      // second scenario: 401, re-exchange ok, 401 again
      json(401, { code: 'unauthorized', message: 'expired', request_id: 'r2' }),
      json(200, sessionBody({ session_token: 'session-3' })),
      json(401, { code: 'unauthorized', message: 'still expired', request_id: 'r3' }),
    ]);
    const changes: unknown[] = [];
    const ml = await MiniLoyal.init({ ...options, fetch, onSessionChange: (s) => changes.push(s) });

    const me = await ml.me();
    expect(me.display_name).toBe('Mei 2');
    expect(header(calls, 1, 'Authorization')).toBe('Bearer session-1');
    expect(header(calls, 3, 'Authorization')).toBe('Bearer session-2');

    await expect(ml.me()).rejects.toMatchObject({
      code: 'unauthorized',
      status: 401,
      requestId: 'r3',
    });
    expect(ml.state).toBe('signed_out');
    expect(changes.at(-1)).toBeNull();
    expect(calls).toHaveLength(7);
  });

  it('reuses the Idempotency-Key across the automatic retry', async () => {
    const { fetch, calls } = scriptedFetch([
      json(200, sessionBody()),
      json(401, { code: 'unauthorized', message: 'expired', request_id: 'r1' }),
      json(200, sessionBody({ session_token: 'session-2' })),
      json(200, {
        ...member,
        consent: { version: '2026-11', accepted_at: '2026-11-09T12:00:00Z' },
      }),
    ]);
    const ml = await MiniLoyal.init({ ...options, fetch });

    const updated = await ml.consent('2026-11');
    expect(updated.consent?.version).toBe('2026-11');
    const first = header(calls, 1, 'Idempotency-Key');
    const retry = header(calls, 3, 'Idempotency-Key');
    expect(first).toMatch(/^[0-9a-f-]{32,36}$/);
    expect(retry).toBe(first);
    expect(calls[3]?.init.method).toBe('POST');
    expect(ml.session?.member.consent?.version).toBe('2026-11');
  });

  it('honours a caller-supplied Idempotency-Key and generates distinct keys otherwise', async () => {
    const { fetch, calls } = scriptedFetch([
      json(200, sessionBody()),
      json(200, member),
      json(200, member),
      json(200, member),
    ]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    await ml.updateMe({ display_name: 'A' }, { idempotencyKey: 'my-key' });
    await ml.updateMe({ display_name: 'B' });
    await ml.updateMe({ display_name: 'C' });
    expect(header(calls, 1, 'Idempotency-Key')).toBe('my-key');
    expect(header(calls, 2, 'Idempotency-Key')).not.toBe(header(calls, 3, 'Idempotency-Key'));
    expect(calls[1]?.init.method).toBe('PATCH');
  });

  it('shares one exchange between concurrent callers', async () => {
    const { fetch, calls } = scriptedFetch([
      json(200, sessionBody()),
      json(200, member),
      json(200, member),
    ]);
    liffMock.isLoggedIn.mockReturnValue(false);
    const ml = await MiniLoyal.init({ ...options, fetch, autoLogin: false });
    liffMock.isLoggedIn.mockReturnValue(true);

    await Promise.all([ml.me(), ml.me()]);
    expect(calls.filter((c) => c.url.endsWith('/v1/auth/line'))).toHaveLength(1);
  });
});

describe('member methods', () => {
  it('addFriendUrl depends on the app basic ID', async () => {
    const { fetch } = scriptedFetch([json(200, sessionBody())]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    expect(ml.addFriendUrl()).toBe('https://line.me/R/ti/p/%40demo');

    const noId = scriptedFetch([
      json(
        200,
        sessionBody({ app: { id: member.app_id, name: 'X', locale: 'en', oa_basic_id: null } }),
      ),
    ]);
    const ml2 = await MiniLoyal.init({ ...options, fetch: noId.fetch });
    expect(ml2.addFriendUrl()).toBeNull();
  });

  it('friendship reads liff.getFriendship and falls back to false', async () => {
    const { fetch } = scriptedFetch([json(200, sessionBody())]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    expect(await ml.friendship()).toBe(true);
    liffMock.getFriendship.mockRejectedValueOnce(new Error('no linked OA'));
    expect(await ml.friendship()).toBe(false);
  });

  it('deleteMe erases and clears the session; exportMe returns the document', async () => {
    const { fetch, calls } = scriptedFetch([
      json(200, sessionBody()),
      json(200, {
        member: { ...member, line_user_id: 'U1', updated_at: '2026-11-09T12:00:00Z' },
        consents: [],
        exported_at: '2026-11-09T12:00:00Z',
      }),
      new Response(null, { status: 204 }),
    ]);
    const changes: unknown[] = [];
    const ml = await MiniLoyal.init({ ...options, fetch, onSessionChange: (s) => changes.push(s) });

    const exported = await ml.exportMe();
    expect(exported.member.line_user_id).toBe('U1');
    await ml.deleteMe();
    expect(calls[2]?.init.method).toBe('DELETE');
    expect(ml.state).toBe('signed_out');
    expect(changes.at(-1)).toBeNull();
  });

  it('logout clears the session and only signs out of LIFF when asked', async () => {
    const { fetch } = scriptedFetch([json(200, sessionBody())]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    ml.logout();
    expect(ml.state).toBe('signed_out');
    expect(liffMock.logout).not.toHaveBeenCalled();
    ml.logout({ liff: true });
    expect(liffMock.logout).toHaveBeenCalledTimes(1);
  });
});

describe('errors', () => {
  it('maps the error envelope to MiniLoyalError with field details', async () => {
    const { fetch } = scriptedFetch([
      json(200, sessionBody()),
      json(400, {
        code: 'validation_failed',
        message: 'request is invalid',
        request_id: 'req-9',
        details: { phone: 'must look like a phone number' },
      }),
    ]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    const err = await ml.updateMe({ phone: 'nope' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MiniLoyalError);
    const e = err as MiniLoyalError;
    expect(e.code).toBe('validation_failed');
    expect(e.status).toBe(400);
    expect(e.requestId).toBe('req-9');
    expect(e.field('phone')).toBe('must look like a phone number');
    expect(ml.state).toBe('signed_in');
  });

  it('turns network failures into code network and non-envelope bodies into http codes', async () => {
    const { fetch } = scriptedFetch([
      json(200, sessionBody()),
      () => {
        throw new TypeError('Failed to fetch');
      },
      new Response('<html>bad gateway</html>', {
        status: 502,
        headers: { 'X-Request-ID': 'edge-1' },
      }),
    ]);
    const ml = await MiniLoyal.init({ ...options, fetch });
    await expect(ml.me()).rejects.toMatchObject({ code: 'network', status: 0 });
    await expect(ml.me()).rejects.toMatchObject({
      code: 'http_502',
      status: 502,
      requestId: 'edge-1',
    });
  });

  it('refuses sign-in responses that are not a session', async () => {
    const { fetch } = scriptedFetch([json(200, { nope: true })]);
    await expect(MiniLoyal.init({ ...options, fetch })).rejects.toMatchObject({
      code: 'invalid_response',
    });
  });
});
