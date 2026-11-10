import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { MemberCard, PointsHistoryPage, PointsSummary } from '../src/index';
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

const appId = 'f7c1d2e3-0000-4000-8000-000000000000';
const memberId = 'f7c1d2e3-0000-4000-8000-000000000001';

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const session = {
  session_token: 'session-1',
  token_type: 'Bearer',
  expires_in: 3600,
  member: {
    id: memberId,
    app_id: appId,
    is_friend: true,
    custom_fields: {},
    created_at: '2026-11-09T12:00:00Z',
  },
  app: { id: appId, name: 'Demo Cafe', locale: 'zh-TW' },
  is_new: false,
};

function card(n: number, refresh = 20): MemberCard {
  return {
    qr: `ml1.payload-${n}`,
    expires_at: '2026-11-09T12:01:00Z',
    refresh_in_seconds: refresh,
    window_seconds: 30,
  };
}

/**
 * A fetch that answers sign-in with a session and every other request from
 * `handler`, recording the paths.
 */
function routedFetch(handler: (path: string) => Response) {
  const paths: string[] = [];
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input));
    const path = url.pathname + url.search;
    if (url.pathname === '/v1/auth/line') return json(200, session);
    paths.push(path);
    return handler(path);
  });
  return { fetch: fetch as unknown as typeof globalThis.fetch, paths };
}

/** A minimal document whose visibility the test controls. */
class FakeDocument extends EventTarget {
  visibilityState: 'visible' | 'hidden' = 'visible';
  setHidden(hidden: boolean) {
    this.visibilityState = hidden ? 'hidden' : 'visible';
    this.dispatchEvent(new Event('visibilitychange'));
  }
}

async function client(fetch: typeof globalThis.fetch): Promise<MiniLoyal> {
  return MiniLoyal.init({
    appId,
    liffId: '1661234567-AbCdEfGh',
    baseUrl: 'https://api.test',
    fetch,
  });
}

/** Lets pending promise callbacks run without moving the fake clock. */
async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}

let doc: FakeDocument;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-11-09T12:00:00Z'));
  vi.spyOn(Math, 'random').mockReturnValue(0);
  doc = new FakeDocument();
  vi.stubGlobal('document', doc);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe('card', () => {
  it('returns the current card', async () => {
    const { fetch, paths } = routedFetch(() => json(200, card(1)));
    const ml = await client(fetch);
    await expect(ml.card()).resolves.toEqual(card(1));
    expect(paths).toEqual(['/v1/me/card']);
  });
});

describe('watchCard', () => {
  it('refreshes after refresh_in_seconds', async () => {
    let n = 0;
    const { fetch, paths } = routedFetch(() => json(200, card(++n, 20)));
    const ml = await client(fetch);
    const seen: string[] = [];
    const stop = ml.watchCard((c) => seen.push(c.qr));
    await flush();
    expect(seen).toEqual(['ml1.payload-1']);

    await vi.advanceTimersByTimeAsync(19_999);
    expect(seen).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(seen).toEqual(['ml1.payload-1', 'ml1.payload-2']);
    stop();
    expect(paths.every((p) => p === '/v1/me/card')).toBe(true);
  });

  it('never refreshes faster than once a second', async () => {
    let n = 0;
    const { fetch } = routedFetch(() => json(200, card(++n, 0)));
    const ml = await client(fetch);
    const seen: string[] = [];
    const stop = ml.watchCard((c) => seen.push(c.qr));
    await flush();
    await vi.advanceTimersByTimeAsync(999);
    expect(seen).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(seen).toHaveLength(2);
    stop();
  });

  it('pauses while hidden and refreshes at once when visible', async () => {
    let n = 0;
    const { fetch } = routedFetch(() => json(200, card(++n, 20)));
    const ml = await client(fetch);
    const seen: string[] = [];
    const stop = ml.watchCard((c) => seen.push(c.qr));
    await flush();
    expect(seen).toHaveLength(1);

    doc.setHidden(true);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(seen).toHaveLength(1);

    doc.setHidden(false);
    await flush();
    expect(seen).toEqual(['ml1.payload-1', 'ml1.payload-2']);
    stop();
  });

  it('retries with backoff and reports errors without throwing', async () => {
    let calls = 0;
    const { fetch } = routedFetch(() => {
      calls++;
      if (calls <= 5) return json(503, { code: 'unavailable', message: 'down', request_id: 'r1' });
      return json(200, card(calls, 20));
    });
    const ml = await client(fetch);
    const errors: MiniLoyalError[] = [];
    const seen: string[] = [];
    const stop = ml.watchCard(
      (c) => seen.push(c.qr),
      (e) => errors.push(e),
    );
    await flush();
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(MiniLoyalError);
    expect(errors[0]?.code).toBe('unavailable');

    // Retries after 1, 2, 4 and 8 seconds.
    for (const [delay, expected] of [
      [1_000, 2],
      [2_000, 3],
      [4_000, 4],
      [8_000, 5],
    ] as const) {
      await vi.advanceTimersByTimeAsync(delay - 1);
      expect(errors).toHaveLength(expected - 1);
      await vi.advanceTimersByTimeAsync(1);
      expect(errors).toHaveLength(expected);
    }
    // The sixth attempt (16 s later) succeeds and the backoff resets.
    await vi.advanceTimersByTimeAsync(16_000);
    expect(seen).toEqual(['ml1.payload-6']);
    stop();
  });

  it('caps the backoff at 30 seconds', async () => {
    const { fetch } = routedFetch(() =>
      json(500, { code: 'internal', message: 'x', request_id: 'r' }),
    );
    const ml = await client(fetch);
    const errors: MiniLoyalError[] = [];
    const stop = ml.watchCard(
      () => {},
      (e) => errors.push(e),
    );
    await flush();
    await vi.advanceTimersByTimeAsync(1_000 + 2_000 + 4_000 + 8_000 + 16_000);
    expect(errors).toHaveLength(6);
    await vi.advanceTimersByTimeAsync(29_999);
    expect(errors).toHaveLength(6);
    await vi.advanceTimersByTimeAsync(1);
    expect(errors).toHaveLength(7);
    stop();
  });

  it('stop() cancels timers and listeners', async () => {
    let n = 0;
    const { fetch, paths } = routedFetch(() => json(200, card(++n, 5)));
    const ml = await client(fetch);
    const stop = ml.watchCard(() => {});
    await flush();
    stop();
    await vi.advanceTimersByTimeAsync(60_000);
    doc.setHidden(true);
    doc.setHidden(false);
    await flush();
    expect(paths).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('does not deliver a card that arrives after stop()', async () => {
    let release: (r: Response) => void = () => {};
    const { fetch } = routedFetch(() => json(200, card(1)));
    const slow = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (new URL(String(input)).pathname === '/v1/me/card') {
        return new Promise<Response>((resolve) => {
          release = resolve;
        });
      }
      return fetch(input, init);
    });
    const ml = await client(slow as unknown as typeof globalThis.fetch);
    const seen: string[] = [];
    const stop = ml.watchCard((c) => seen.push(c.qr));
    await flush();
    stop();
    release(json(200, card(1)));
    await flush();
    expect(seen).toEqual([]);
  });
});

/**
 * A fetch whose /v1/me/card responses the test settles by hand, one per
 * request, so it can stop the watcher while a request is in flight.
 */
function heldFetch() {
  const { fetch } = routedFetch(() => json(200, card(0)));
  const pending: Array<{ resolve: (r: Response) => void; reject: (e: unknown) => void }> = [];
  const held = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (new URL(String(input)).pathname === '/v1/me/card') {
      return new Promise<Response>((resolve, reject) => pending.push({ resolve, reject }));
    }
    return fetch(input, init);
  });
  const cardRequests = () =>
    held.mock.calls.filter(([input]) => new URL(String(input)).pathname === '/v1/me/card').length;
  return { fetch: held as unknown as typeof globalThis.fetch, pending, cardRequests };
}

describe('watchCard stopped during an in-flight fetch', () => {
  it('leaves no timer and fetches nothing more when the fetch then succeeds', async () => {
    const { fetch, pending, cardRequests } = heldFetch();
    const ml = await client(fetch);
    const seen: string[] = [];
    const errors: MiniLoyalError[] = [];
    const stop = ml.watchCard(
      (c) => seen.push(c.qr),
      (e) => errors.push(e),
    );
    await flush();
    expect(cardRequests()).toBe(1);
    stop();
    pending[0]?.resolve(json(200, card(1, 1)));
    await flush();
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(seen).toEqual([]);
    expect(errors).toEqual([]);
    expect(cardRequests()).toBe(1);
  });

  it('reports nothing and schedules no retry when the fetch then fails', async () => {
    const { fetch, pending, cardRequests } = heldFetch();
    const ml = await client(fetch);
    const errors: MiniLoyalError[] = [];
    const stop = ml.watchCard(
      () => {},
      (e) => errors.push(e),
    );
    await flush();
    stop();
    pending[0]?.reject(new TypeError('network down'));
    await flush();
    expect(errors).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(cardRequests()).toBe(1);
  });

  it('ignores visibility changes after stop, even with a fetch still in flight', async () => {
    const { fetch, pending, cardRequests } = heldFetch();
    const ml = await client(fetch);
    const seen: string[] = [];
    const stop = ml.watchCard((c) => seen.push(c.qr));
    await flush();
    stop();
    doc.setHidden(true);
    doc.setHidden(false);
    await flush();
    pending[0]?.resolve(json(200, card(1)));
    await flush();
    expect(cardRequests()).toBe(1);
    expect(seen).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('never runs two fetches at once when the page becomes visible mid-fetch', async () => {
    const { fetch, pending, cardRequests } = heldFetch();
    const ml = await client(fetch);
    const seen: string[] = [];
    const stop = ml.watchCard((c) => seen.push(c.qr));
    await flush();
    doc.setHidden(true);
    doc.setHidden(false);
    await flush();
    expect(cardRequests()).toBe(1);
    pending[0]?.resolve(json(200, card(1, 20)));
    await flush();
    expect(seen).toEqual(['ml1.payload-1']);
    await vi.advanceTimersByTimeAsync(20_000);
    expect(cardRequests()).toBe(2);
    stop();
    pending[1]?.resolve(json(200, card(2, 20)));
    await flush();
    expect(seen).toEqual(['ml1.payload-1']);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('can be stopped twice', async () => {
    const { fetch } = heldFetch();
    const ml = await client(fetch);
    const stop = ml.watchCard(() => {});
    await flush();
    stop();
    expect(() => stop()).not.toThrow();
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe('points', () => {
  const summary: PointsSummary = {
    member_id: memberId,
    balance: 120,
    lifetime_earned: 300,
    expiring: [{ points: 20, expires_at: '2026-12-01T00:00:00Z' }],
    rules: {
      currency: 'TWD',
      spend_per_point: 100,
      points_per_unit: 1,
      expiry_days: 365,
      enabled: true,
    },
  };

  it('returns the summary', async () => {
    const { fetch, paths } = routedFetch(() => json(200, summary));
    const ml = await client(fetch);
    await expect(ml.points()).resolves.toEqual(summary);
    expect(paths).toEqual(['/v1/me/points']);
  });

  it('maps API errors to MiniLoyalError', async () => {
    const { fetch } = routedFetch(() =>
      json(404, { code: 'not_found', message: 'member not found', request_id: 'req-9' }),
    );
    const ml = await client(fetch);
    const err = await ml.points().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MiniLoyalError);
    expect(err).toMatchObject({ code: 'not_found', status: 404, requestId: 'req-9' });
  });

  it('pages the history with limit and cursor', async () => {
    const page: PointsHistoryPage = { items: [], next_cursor: null };
    const { fetch, paths } = routedFetch(() => json(200, page));
    const ml = await client(fetch);
    await ml.pointsHistory();
    await ml.pointsHistory({ limit: 10, cursor: 'abc/=' });
    expect(paths).toEqual([
      '/v1/me/points/history',
      '/v1/me/points/history?limit=10&cursor=abc%2F%3D',
    ]);
  });

  it('maps history errors to MiniLoyalError', async () => {
    const { fetch } = routedFetch(() =>
      json(400, {
        code: 'bad_request',
        message: 'bad cursor',
        request_id: 'r',
        details: { cursor: 'invalid' },
      }),
    );
    const ml = await client(fetch);
    const err = await ml.pointsHistory({ cursor: 'x' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MiniLoyalError);
    expect((err as MiniLoyalError).field('cursor')).toBe('invalid');
  });

  it('pointsHistoryAll walks pages until the cursor is null', async () => {
    const tx = {
      id: '00000000-0000-4000-8000-0000000000aa',
      member_id: memberId,
      kind: 'earn' as const,
      points: 5,
      balance_after: 5,
      actor_type: 'staff' as const,
      created_at: '2026-11-09T12:00:00Z',
    };
    const pages: PointsHistoryPage[] = [
      { items: [tx], next_cursor: 'c1' },
      { items: [tx], next_cursor: 'c2' },
      { items: [tx], next_cursor: null },
    ];
    const { fetch, paths } = routedFetch(() => json(200, pages.shift()));
    const ml = await client(fetch);
    let count = 0;
    for await (const page of ml.pointsHistoryAll({ limit: 1 })) count += page.items.length;
    expect(count).toBe(3);
    expect(paths).toEqual([
      '/v1/me/points/history?limit=1',
      '/v1/me/points/history?limit=1&cursor=c1',
      '/v1/me/points/history?limit=1&cursor=c2',
    ]);
  });
});
