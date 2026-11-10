import { useSyncExternalStore } from 'react';

import { api, setSessionHandlers, toApiError, tokenStore, unwrap } from '../api/client';
import type { StaffSession } from '../api/types';
import { config, configured } from '../config';
import { liff } from '../liff';

/** Where the staff app is in signing in. */
export type SessionState =
  | { status: 'starting' }
  | { status: 'redirecting' }
  | {
      status: 'needs_invite';
      /** Why the last attempt failed, when the cashier already typed a code. */
      error?: 'wrong_code' | 'rate_limited' | 'network';
      retryAfterSeconds?: number;
    }
  | { status: 'rate_limited'; retryAfterSeconds: number }
  | {
      status: 'error';
      reason: 'not_configured' | 'liff_init' | 'no_id_token' | 'app_not_found' | 'server';
    }
  | { status: 'signed_in'; session: StaffSession }
  | { status: 'signed_out'; reason: 'signed_out' | 'expired' };

let state: SessionState = { status: 'starting' };
const listeners = new Set<() => void>();

function set(next: SessionState) {
  state = next;
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** The current sign-in state. */
export function getSession(): SessionState {
  return state;
}

/** Subscribes a component to the sign-in state. */
export function useSession(): SessionState {
  return useSyncExternalStore(subscribe, getSession, getSession);
}

type ExchangeResult =
  { ok: true; session: StaffSession } | { ok: false; status: number; retryAfterSeconds?: number };

/** Normalizes a typed invite code: the API ignores dashes, spaces and case. */
export function normalizeInviteCode(code: string): string {
  return code.replace(/[\s-]/g, '').toUpperCase();
}

async function exchange(inviteCode?: string): Promise<ExchangeResult> {
  const idToken = liff.getIDToken();
  if (!idToken) return { ok: false, status: -1 };
  try {
    const session = unwrap(
      await api.POST('/v1/staff/auth/line', {
        body: {
          app_id: config.appId,
          id_token: idToken,
          ...(inviteCode ? { invite_code: normalizeInviteCode(inviteCode) } : {}),
        },
      }),
    );
    return { ok: true, session };
  } catch (err) {
    const e = toApiError(err);
    return { ok: false, status: e.status, retryAfterSeconds: e.retryAfterSeconds };
  }
}

function signedIn(session: StaffSession) {
  tokenStore.set(session.session_token);
  set({ status: 'signed_in', session });
}

function handleStartFailure(result: Extract<ExchangeResult, { ok: false }>) {
  switch (result.status) {
    case -1:
      return set({ status: 'error', reason: 'no_id_token' });
    case 401:
      // Unknown LINE account for this app: the first sign-in needs the code.
      return set({ status: 'needs_invite' });
    case 404:
      return set({ status: 'error', reason: 'app_not_found' });
    case 429:
      return set({ status: 'rate_limited', retryAfterSeconds: result.retryAfterSeconds ?? 60 });
    default:
      return set({ status: 'error', reason: 'server' });
  }
}

let starting: Promise<void> | null = null;

/**
 * Initializes LIFF, sends the cashier to LINE login if needed, and signs in.
 * Concurrent calls (React StrictMode runs effects twice) share one attempt.
 */
export function start(): Promise<void> {
  starting ??= doStart().finally(() => {
    starting = null;
  });
  return starting;
}

async function doStart(): Promise<void> {
  if (!configured()) {
    set({ status: 'error', reason: 'not_configured' });
    return;
  }
  set({ status: 'starting' });
  try {
    await liff.init({ liffId: config.liffId });
  } catch {
    set({ status: 'error', reason: 'liff_init' });
    return;
  }
  if (!liff.isLoggedIn()) {
    set({ status: 'redirecting' });
    liff.login();
    return;
  }
  const result = await exchange();
  if (result.ok) signedIn(result.session);
  else handleStartFailure(result);
}

/** Retries sign-in with the invite code from the dashboard (first sign-in). */
export async function submitInviteCode(code: string): Promise<void> {
  const result = await exchange(code);
  if (result.ok) {
    signedIn(result.session);
    return;
  }
  switch (result.status) {
    case 401:
      return set({ status: 'needs_invite', error: 'wrong_code' });
    case 429:
      return set({
        status: 'needs_invite',
        error: 'rate_limited',
        retryAfterSeconds: result.retryAfterSeconds ?? 60,
      });
    case 0:
      return set({ status: 'needs_invite', error: 'network' });
    default:
      return handleStartFailure(result);
  }
}

/** Re-exchanges a fresh ID token after a 401; used once per failed request. */
async function reauth(): Promise<string | null> {
  const result = await exchange();
  if (!result.ok) return null;
  signedIn(result.session);
  return result.session.session_token;
}

/** Ends the session on this device. The next cashier signs in with their own LINE. */
export function signOut(): void {
  tokenStore.clear();
  try {
    if (liff.isLoggedIn()) liff.logout();
  } catch {
    // Not every LINE client supports logout; the in-memory session is gone either way.
  }
  set({ status: 'signed_out', reason: 'signed_out' });
}

setSessionHandlers(reauth, () => set({ status: 'signed_out', reason: 'expired' }));

/** Test helper: back to the initial state. */
export function resetSessionForTests(): void {
  tokenStore.clear();
  set({ status: 'starting' });
}
