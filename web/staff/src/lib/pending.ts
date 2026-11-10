import { useSyncExternalStore } from 'react';

import type { ApiError } from '../api/client';
import type { PointsTransaction, ScannedMember } from '../api/types';

/** The request body of an earn or a redemption. */
export type EarnBody =
  { scan_token: string; spend_amount: number } | { scan_token: string; points: number };
export type RedeemBody = { scan_token: string; points: number };

/**
 * A points write whose outcome is not known yet. It keeps its Idempotency-Key
 * and body until the server gives a definite answer, so a retry after a lost
 * response replays the same write instead of making a second one.
 */
export type PendingAction = {
  key: string;
  staffId: string;
  member: ScannedMember;
  /** The last failure, shown in the banner. */
  error?: ApiError;
} & ({ kind: 'earn'; body: EarnBody } | { kind: 'redeem'; body: RedeemBody });

/** What a submission came back with. */
export type Outcome =
  { ok: true; txn: PointsTransaction } | { ok: false; error: ApiError; ambiguous: boolean };

/**
 * Whether a failure says for certain that the write did not happen. Errors
 * are never stored as outcomes, so a 4xx means nothing was written. Network
 * errors, 5xx, 408, 429 and "still in progress" do not tell: the write may
 * have been applied and only the answer lost. A 401 means the session could
 * not be renewed; the write is kept too.
 */
export function isDefinite(err: ApiError): boolean {
  const s = err.status;
  if (s < 400 || s >= 500) return false;
  return s !== 401 && s !== 408 && s !== 429 && err.code !== 'conflict';
}

// The pending action lives in memory outside the counter, so it survives the
// panel expiring, a new scan and a session that had to be signed in again.
// It is never written to storage: the body carries a scan token.
let pending: PendingAction | null = null;
const listeners = new Set<() => void>();

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function getPendingAction(): PendingAction | null {
  return pending;
}

export function setPendingAction(next: PendingAction | null): void {
  pending = next;
  listeners.forEach((l) => l());
}

/** The pending action of this staff member, if any. */
export function usePendingAction(staffId: string): PendingAction | null {
  const p = useSyncExternalStore(subscribe, getPendingAction, getPendingAction);
  return p && p.staffId === staffId ? p : null;
}

/** Test helper: forget any pending action. */
export function resetPendingForTests(): void {
  pending = null;
}
