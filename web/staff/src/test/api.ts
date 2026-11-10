import { vi } from 'vitest';

import type {
  CouponInstance,
  PointsTransaction,
  ScanResult,
  StampProgress,
  StaffSession,
} from '../api/types';

export function coupon(
  id = 'c1',
  name = 'Free latte',
  expires: string | null = null,
): CouponInstance {
  return {
    id: `0193a0b4-0000-7000-8000-0000000c${id.padEnd(4, '0').slice(-4)}`,
    code: `CODE-${id}`,
    template: { id: 't1', name, description: null, type: 'free_item', value: null },
    member_id: '0193a0b4-0000-7000-8000-0000000000cc',
    status: 'issued',
    state: 'available',
    source: 'issue',
    issued_at: new Date().toISOString(),
    expires_at: expires,
  };
}

export function stampCard(count = 3, max = 3): StampProgress {
  return {
    stamp_card_id: '0193a0b4-0000-7000-8000-0000000000e1',
    name: 'Coffee card',
    stamps_required: 5,
    max_stamps_per_visit: max,
    status: 'active',
    count,
    completions: 0,
    unrewarded_completions: 0,
    reward: { id: 't2', name: 'Free cake', description: null, type: 'free_item', value: null },
  };
}

export const APP_ID = '0193a0b4-0000-7000-8000-000000000001';

/** One request the app sent, as the fake server saw it. */
export interface Call {
  method: string;
  path: string;
  headers: Headers;
  body: Record<string, unknown> | undefined;
}

type Handler = (call: Call, n: number) => Response | Promise<Response>;

export function json(
  status: number,
  body: unknown,
  headers: Record<string, string> = {},
): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

export function errorBody(code: string, message = code, details?: Record<string, unknown>) {
  return { code, message, request_id: 'req-1', ...(details ? { details } : {}) };
}

/**
 * Replaces fetch with a route table keyed "METHOD /path". Each handler gets
 * the call and how many times that route was hit before (0 for the first).
 */
export function mockApi(routes: Record<string, Handler>) {
  const calls: Call[] = [];
  const hits = new Map<string, number>();
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const request = input instanceof Request ? input : new Request(input);
    const url = new URL(request.url);
    const text = await request.text();
    const call: Call = {
      method: request.method,
      path: url.pathname,
      headers: request.headers,
      body: text ? (JSON.parse(text) as Record<string, unknown>) : undefined,
    };
    calls.push(call);
    const key = `${call.method} ${call.path}`;
    const n = hits.get(key) ?? 0;
    hits.set(key, n + 1);
    const handler = routes[key];
    if (!handler) return json(404, errorBody('not_found', `no route ${key}`));
    return handler(call, n);
  });
  vi.stubGlobal('fetch', fetchMock);
  return {
    calls,
    to: (method: string, path: string) =>
      calls.filter((c) => c.method === method && c.path === path),
  };
}

export function staffSession(
  role: 'cashier' | 'manager' = 'cashier',
  token = 'staff-token-1',
): StaffSession {
  return {
    session_token: token,
    token_type: 'Bearer',
    expires_in: 43200,
    staff: {
      id: '0193a0b4-0000-7000-8000-0000000000aa',
      app_id: APP_ID,
      display_name: 'Mei',
      role,
      store: { id: '0193a0b4-0000-7000-8000-0000000000bb', name: 'Zhongxiao' },
    },
    app: { id: APP_ID, name: 'Demo Cafe', locale: 'zh-TW' },
  };
}

export function scanResult(secondsLeft = 300, balance = 120): ScanResult {
  return {
    scan_token: 'st1.scan-token',
    expires_at: new Date(Date.now() + secondsLeft * 1000).toISOString(),
    member: {
      id: '0193a0b4-0000-7000-8000-0000000000cc',
      display_name: 'Lin',
      picture_url: null,
      is_friend: true,
    },
    balance,
    coupons: [],
    stamps: [],
  };
}

export function txn(points: number, balanceAfter: number): PointsTransaction {
  return {
    id: '0193a0b4-0000-7000-8000-0000000000dd',
    member_id: '0193a0b4-0000-7000-8000-0000000000cc',
    kind: points >= 0 ? 'earn' : 'redeem',
    points,
    balance_after: balanceAfter,
    actor_type: 'staff',
    created_at: new Date().toISOString(),
  };
}
