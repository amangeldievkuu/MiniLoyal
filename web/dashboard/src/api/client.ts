import createClient from 'openapi-fetch';

import type { paths } from './schema';
import type { ApiErrorBody, Session } from './types';

/** API origin. Empty means same origin (the Vite dev proxy forwards /v1). */
export const baseUrl = (import.meta.env.VITE_API_URL ?? '').replace(/\/$/, '');

/** A failed API call, carrying the Error envelope when the server sent one. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;
  readonly details: Record<string, unknown> | undefined;

  constructor(status: number, body: Partial<ApiErrorBody> | undefined, fallback: string) {
    super(body?.message ?? fallback);
    this.name = 'ApiError';
    this.status = status;
    this.code = body?.code ?? (status === 0 ? 'network' : `http_${status}`);
    this.requestId = body?.request_id;
    this.details = body?.details;
  }

  /** The message for one field from `details`, when the server sent one. */
  field(name: string): string | undefined {
    const v = this.details?.[name];
    return typeof v === 'string' ? v : undefined;
  }
}

/** Narrows unknown errors thrown by the client. */
export function asApiError(err: unknown): ApiError | null {
  return err instanceof ApiError ? err : null;
}

// ---------------------------------------------------------------- token store

type Listener = () => void;

function createTokenStore() {
  let token: string | null = null;
  const listeners = new Set<Listener>();
  return {
    get: () => token,
    set(next: string | null) {
      token = next;
      listeners.forEach((l) => l());
    },
    clear() {
      this.set(null);
    },
    subscribe(l: Listener) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
  };
}

/** The access token lives only in memory. */
export const tokenStore = createTokenStore();

const sessionLostListeners = new Set<Listener>();

/** Called when a refresh fails: the cookie is gone or expired. */
export function onSessionLost(l: Listener): () => void {
  sessionLostListeners.add(l);
  return () => sessionLostListeners.delete(l);
}

// -------------------------------------------------------------------- refresh

let refreshing: Promise<Session | null> | null = null;

/**
 * Exchanges the refresh cookie for a new session. Concurrent callers share one
 * request. Returns null when the server refuses.
 */
export function refreshSession(): Promise<Session | null> {
  refreshing ??= (async () => {
    try {
      const res = await fetch(`${baseUrl}/v1/dashboard/auth/refresh`, {
        method: 'POST',
        credentials: 'include',
        headers: { Accept: 'application/json' },
      });
      if (!res.ok) return null;
      const session = (await res.json()) as Session;
      tokenStore.set(session.access_token);
      return session;
    } catch {
      return null;
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

function isAuthPath(url: string): boolean {
  return new URL(url, 'http://local').pathname.startsWith('/v1/dashboard/auth/');
}

function withBearer(request: Request, token: string | null): Request {
  const headers = new Headers(request.headers);
  if (token) headers.set('Authorization', `Bearer ${token}`);
  else headers.delete('Authorization');
  return new Request(request, { headers });
}

/**
 * fetch with the bearer token attached. A 401 on a protected route triggers one
 * shared refresh and one retry; when the refresh fails the session is cleared
 * and session-lost listeners run (the app routes to /login).
 */
export async function authFetch(request: Request): Promise<Response> {
  const protectedRoute = !isAuthPath(request.url);
  const retryCopy = protectedRoute ? request.clone() : null;
  const response = await fetch(withBearer(request, tokenStore.get()));
  if (response.status !== 401 || !retryCopy) return response;

  const session = await refreshSession();
  if (!session) {
    tokenStore.clear();
    sessionLostListeners.forEach((l) => l());
    return response;
  }
  return fetch(withBearer(retryCopy, session.access_token));
}

// --------------------------------------------------------------------- client

export const api = createClient<paths>({ baseUrl, credentials: 'include', fetch: authFetch });

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
    throw new ApiError(response.status, body, response.statusText || 'request failed');
  }
  return data as T;
}

/** A fresh Idempotency-Key. */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

/**
 * Keeps one Idempotency-Key per logical submission: the same key is sent on a
 * retry after a failure and a new one is minted once the request succeeds.
 */
export function createIdempotencyKeyHolder() {
  let current: string | null = null;
  return {
    /** The key for the attempt in progress. */
    key(): string {
      current ??= newIdempotencyKey();
      return current;
    },
    /** Call after success so the next submission gets a new key. */
    reset() {
      current = null;
    },
  };
}
