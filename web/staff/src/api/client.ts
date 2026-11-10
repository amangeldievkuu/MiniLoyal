import createClient from 'openapi-fetch';

import { config } from '../config';
import type { paths } from './schema';
import type { ApiErrorBody } from './types';

/** A failed API call, carrying the Error envelope when the server sent one. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;
  readonly details: Record<string, unknown> | undefined;
  /** Seconds to wait before retrying (429), from the body or the Retry-After header. */
  readonly retryAfterSeconds: number | undefined;

  constructor(
    status: number,
    body: Partial<ApiErrorBody> | undefined,
    fallback: string,
    retryAfterHeader?: string | null,
  ) {
    super(body?.message ?? fallback);
    this.name = 'ApiError';
    this.status = status;
    this.code = body?.code ?? (status === 0 ? 'network' : `http_${status}`);
    this.requestId = body?.request_id;
    this.details = body?.details;
    const fromBody = body?.details?.retry_after_seconds;
    const fromHeader = retryAfterHeader ? Number.parseInt(retryAfterHeader, 10) : Number.NaN;
    this.retryAfterSeconds =
      typeof fromBody === 'number'
        ? fromBody
        : Number.isFinite(fromHeader)
          ? fromHeader
          : undefined;
  }

  /** A numeric value from `details`, when the server sent one. */
  detailNumber(name: string): number | undefined {
    const v = this.details?.[name];
    return typeof v === 'number' ? v : undefined;
  }
}

/** Narrows unknown errors thrown by the client. */
export function asApiError(err: unknown): ApiError | null {
  return err instanceof ApiError ? err : null;
}

/** Turns anything thrown into an ApiError (fetch failures become `network`). */
export function toApiError(err: unknown): ApiError {
  return (
    asApiError(err) ??
    new ApiError(0, undefined, err instanceof Error ? err.message : 'network error')
  );
}

// ---------------------------------------------------------------- token store

/** The staff session token lives only in memory. */
let token: string | null = null;

export const tokenStore = {
  get: (): string | null => token,
  set(next: string | null) {
    token = next;
  },
  clear() {
    token = null;
  },
};

/** Exchanges a fresh LIFF ID token for a new session; returns the new token. */
type Reauth = () => Promise<string | null>;

let reauthHandler: Reauth | null = null;
let sessionLostHandler: (() => void) | null = null;
let reauthing: Promise<string | null> | null = null;

/** Registers how to re-exchange the ID token and what to do when that fails. */
export function setSessionHandlers(reauth: Reauth | null, onLost: (() => void) | null): void {
  reauthHandler = reauth;
  sessionLostHandler = onLost;
}

const SIGN_IN_PATH = '/v1/staff/auth/line';

function isSignIn(url: string): boolean {
  return new URL(url, 'http://local').pathname === SIGN_IN_PATH;
}

function withBearer(request: Request, bearer: string | null): Request {
  const headers = new Headers(request.headers);
  if (bearer) headers.set('Authorization', `Bearer ${bearer}`);
  else headers.delete('Authorization');
  return new Request(request, { headers });
}

/**
 * fetch with the staff session attached. A 401 on a protected route
 * re-exchanges a fresh LIFF ID token once (concurrent callers share it) and
 * retries the request once; if that fails too the session is lost.
 */
export async function staffFetch(request: Request): Promise<Response> {
  if (isSignIn(request.url)) return globalThis.fetch(withBearer(request, null));
  const retryCopy = request.clone();
  const response = await globalThis.fetch(withBearer(request, tokenStore.get()));
  if (response.status !== 401 || !reauthHandler) return response;

  reauthing ??= reauthHandler().finally(() => {
    reauthing = null;
  });
  const fresh = await reauthing;
  if (!fresh) {
    tokenStore.clear();
    sessionLostHandler?.();
    return response;
  }
  const retried = await globalThis.fetch(withBearer(retryCopy, fresh));
  if (retried.status === 401) {
    tokenStore.clear();
    sessionLostHandler?.();
  }
  return retried;
}

// --------------------------------------------------------------------- client

export const api = createClient<paths>({ baseUrl: config.apiBase, fetch: staffFetch });

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** Turns an openapi-fetch result into data or throws an ApiError. */
export function unwrap<T>(result: Result<T>): T {
  const { data, error, response } = result;
  if (error !== undefined || !response.ok) {
    const body = (error ?? undefined) as Partial<ApiErrorBody> | undefined;
    throw new ApiError(
      response.status,
      typeof body === 'object' ? body : undefined,
      response.statusText || 'request failed',
      response.headers.get('Retry-After'),
    );
  }
  return data as T;
}
