import { useSyncExternalStore } from 'react';

import { api, onSessionLost, refreshSession, tokenStore, unwrap } from '../api/client';
import type { Me, Session } from '../api/types';

export type SessionState =
  { status: 'loading' } | { status: 'anonymous' } | { status: 'authenticated'; me: Me };

let state: SessionState = { status: 'loading' };
const listeners = new Set<() => void>();
let ready: Promise<void> | null = null;

function set(next: SessionState) {
  state = next;
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

function get(): SessionState {
  return state;
}

/** Loads the profile for the current token; clears the session when it fails. */
async function loadMe(): Promise<void> {
  try {
    const me = unwrap(await api.GET('/v1/dashboard/me'));
    set({ status: 'authenticated', me });
  } catch {
    tokenStore.clear();
    set({ status: 'anonymous' });
  }
}

/** Tries the refresh cookie once at startup. Subsequent calls return the same promise. */
function bootstrap(): Promise<void> {
  ready ??= (async () => {
    const session = await refreshSession();
    if (!session) {
      set({ status: 'anonymous' });
      return;
    }
    await loadMe();
  })();
  return ready;
}

/** Starts a session from a login, register, MFA or password-change response. */
async function start(session: Session): Promise<void> {
  tokenStore.set(session.access_token);
  await loadMe();
}

/** Re-reads the profile (after TOTP changes, for example). */
async function reloadMe(): Promise<void> {
  if (state.status !== 'authenticated') return;
  try {
    const me = unwrap(await api.GET('/v1/dashboard/me'));
    set({ status: 'authenticated', me });
  } catch {
    // Keep the stale profile; the next protected call handles expiry.
  }
}

/** Signs out this device. */
async function logout(): Promise<void> {
  try {
    await api.POST('/v1/dashboard/auth/logout');
  } catch {
    // The cookie may already be gone.
  }
  clear();
}

function clear(): void {
  tokenStore.clear();
  set({ status: 'anonymous' });
}

export const sessionStore = { get, subscribe, bootstrap, start, reloadMe, logout, clear };

onSessionLost(clear);

/** The session as React state. */
export function useSession(): SessionState {
  return useSyncExternalStore(subscribe, get, get);
}

/** The signed-in profile, or null. */
export function useMe(): Me | null {
  const s = useSession();
  return s.status === 'authenticated' ? s.me : null;
}
