import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { CouponInstance, CouponOffer, MemberSession, StampProgress } from '../src/index';
import { MiniLoyal } from '../src/index';

vi.mock('@line/liff', () => ({ default: {} }));

const appId = 'f7c1d2e3-0000-4000-8000-000000000000';
const liff = {
  init: vi.fn(async () => {}),
  isLoggedIn: () => true,
  login: vi.fn(),
  logout: vi.fn(),
  getIDToken: () => 'id-token',
  getAccessToken: () => 'access',
  getLanguage: () => 'en',
  getFriendship: async () => ({ friendFlag: true }),
};

const session: MemberSession = {
  session_token: 's1',
  token_type: 'Bearer',
  expires_in: 3600,
  member: {
    id: 'm1',
    app_id: appId,
    is_friend: true,
    custom_fields: {},
    created_at: '2026-11-09T12:00:00Z',
  },
  app: { id: appId, name: 'Cafe', locale: 'en' },
  is_new: false,
};

const template = { id: 't1', name: 'Free latte', type: 'free_item' } as CouponOffer['template'];
const coupon = {
  id: 'c1',
  code: 'ABCD1234',
  template,
  member_id: 'm1',
  status: 'issued',
  state: 'available',
  source: 'claim',
  issued_at: '2026-11-10T00:00:00Z',
  expires_at: '2026-12-10T00:00:00Z',
} as CouponInstance;

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

type Call = { url: string; init: RequestInit };

function setup(responses: Response[]) {
  const calls: Call[] = [];
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    const next = responses.shift();
    if (!next) throw new Error('unexpected fetch ' + String(input));
    return next;
  }) as unknown as typeof globalThis.fetch;
  const init = () =>
    MiniLoyal.init({ appId, liffId: 'l', baseUrl: 'https://api.test', fetch, liff });
  return { calls, init };
}

const headerOf = (c: Call | undefined, name: string) =>
  (c?.init.headers as Record<string, string> | undefined)?.[name];

describe('coupons and stamps', () => {
  beforeEach(() => vi.clearAllMocks());

  it('lists coupons with filter and paging', async () => {
    const { calls, init } = setup([
      json(200, session),
      json(200, { items: [coupon], next_cursor: 'n1' }),
      json(200, { items: [], next_cursor: null }),
    ]);
    const ml = await init();
    const page = await ml.coupons.list({ state: 'available', limit: 5 });
    expect(page.items[0]?.code).toBe('ABCD1234');
    expect(calls[1]?.url).toBe('https://api.test/v1/me/coupons?state=available&limit=5');
    await ml.coupons.list({ cursor: 'n1' });
    expect(calls[2]?.url).toBe('https://api.test/v1/me/coupons?cursor=n1');
  });

  it('walks every page with listAll', async () => {
    const { calls, init } = setup([
      json(200, session),
      json(200, { items: [coupon], next_cursor: 'n1' }),
      json(200, { items: [coupon], next_cursor: null }),
    ]);
    const ml = await init();
    let n = 0;
    for await (const p of ml.coupons.listAll({ state: 'used', limit: 1 })) n += p.items.length;
    expect(n).toBe(2);
    expect(calls[2]?.url).toBe('https://api.test/v1/me/coupons?state=used&limit=1&cursor=n1');
  });

  it('lists offers', async () => {
    const offer = { template, per_member_limit: 1, held: 0, can_claim: true } as CouponOffer;
    const { calls, init } = setup([json(200, session), json(200, [offer])]);
    const ml = await init();
    expect(await ml.coupons.offers()).toEqual([offer]);
    expect(calls[1]?.url).toBe('https://api.test/v1/coupons/offers');
  });

  it('claims with a generated or given idempotency key', async () => {
    const { calls, init } = setup([json(200, session), json(201, coupon), json(201, coupon)]);
    const ml = await init();
    expect((await ml.coupons.claim('t1')).id).toBe('c1');
    expect(calls[1]?.init.method).toBe('POST');
    expect(calls[1]?.url).toBe('https://api.test/v1/coupons/t1/claim');
    expect(headerOf(calls[1], 'Idempotency-Key')).toBeTruthy();
    await ml.coupons.claim('t1', { idempotencyKey: 'mine' });
    expect(headerOf(calls[2], 'Idempotency-Key')).toBe('mine');
  });

  it('reuses the key when the claim is retried after a 401', async () => {
    const unauth = { code: 'unauthorized', message: 'expired', request_id: 'r1' };
    const { calls, init } = setup([
      json(200, session),
      json(401, unauth),
      json(200, { ...session, session_token: 's2' }),
      json(201, coupon),
    ]);
    const ml = await init();
    await ml.coupons.claim('t1');
    expect(calls).toHaveLength(4);
    expect(headerOf(calls[3], 'Idempotency-Key')).toBe(headerOf(calls[1], 'Idempotency-Key'));
    expect(headerOf(calls[3], 'Authorization')).toBe('Bearer s2');
  });

  it.each(['coupon_sold_out', 'coupon_limit_reached', 'coupon_not_claimable'])(
    'maps %s to a typed error',
    async (code) => {
      const { init } = setup([
        json(200, session),
        json(409, { code, message: 'no', request_id: 'req-7' }),
      ]);
      const ml = await init();
      await expect(ml.coupons.claim('t1')).rejects.toMatchObject({
        code,
        status: 409,
        requestId: 'req-7',
      });
    },
  );

  it('reads stamp progress', async () => {
    const progress = [
      { stamp_card_id: 'k1', name: 'Coffee', stamps_required: 8, count: 3, completions: 1 },
    ] as StampProgress[];
    const { calls, init } = setup([json(200, session), json(200, progress)]);
    const ml = await init();
    expect(await ml.stamps()).toEqual(progress);
    expect(calls[1]?.url).toBe('https://api.test/v1/me/stamps');
  });
});
