import { useEffect, useSyncExternalStore } from 'react';

// The tenant the current page belongs to, so the shell can show the right
// navigation. Pages set it; the index route uses the last one as the default.

const STORAGE_KEY = 'ml.tenant';
let current: string | null = readStored();
const listeners = new Set<() => void>();

function readStored(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

function get(): string | null {
  return current;
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

/** Records the tenant the user is working in. */
export function setCurrentTenant(id: string | null): void {
  if (current === id) return;
  current = id;
  try {
    if (id) localStorage.setItem(STORAGE_KEY, id);
    else localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Remembering the tenant is a convenience only.
  }
  listeners.forEach((l) => l());
}

/** The last tenant the user worked in, if any. */
export function lastTenant(): string | null {
  return current;
}

export function useCurrentTenant(): string | null {
  return useSyncExternalStore(subscribe, get, get);
}

/** Declares the tenant a page belongs to. */
export function useDeclareTenant(id: string | null | undefined): void {
  useEffect(() => {
    if (id) setCurrentTenant(id);
  }, [id]);
}
