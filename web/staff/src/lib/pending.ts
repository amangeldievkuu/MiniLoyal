import { useSyncExternalStore } from 'react';

import { api, toApiError, unwrap, type ApiError } from '../api/client';
import type { CouponInstance, PointsTransaction, ScannedMember, StampResult } from '../api/types';

/** The request body of an earn or a redemption. */
export type EarnBody =
  { scan_token: string; spend_amount: number } | { scan_token: string; points: number };
export type RedeemBody = { scan_token: string; points: number };
export type PointsWrite = { kind: 'earn'; body: EarnBody } | { kind: 'redeem'; body: RedeemBody };
export type CouponWrite = {
  kind: 'coupon';
  couponId: string;
  couponName: string;
  body: { scan_token: string };
};
export type StampWrite = {
  kind: 'stamp';
  cardName: string;
  body: { scan_token: string; stamp_card_id: string; count: number };
};
export type AnyWrite = PointsWrite | CouponWrite | StampWrite;

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
} & AnyWrite;

/** What an applied write returned. */
export type Applied =
  | { kind: 'points'; txn: PointsTransaction }
  | { kind: 'coupon'; coupon: CouponInstance }
  | { kind: 'stamp'; result: StampResult };

async function post(p: PendingAction): Promise<Applied> {
  const params = { header: { 'Idempotency-Key': p.key } };
  switch (p.kind) {
    case 'earn':
      return {
        kind: 'points',
        txn: unwrap(await api.POST('/v1/staff/points/earn', { params, body: p.body })),
      };
    case 'redeem':
      return {
        kind: 'points',
        txn: unwrap(await api.POST('/v1/staff/points/redeem', { params, body: p.body })),
      };
    case 'coupon':
      return {
        kind: 'coupon',
        coupon: unwrap(
          await api.POST('/v1/staff/coupons/{instanceId}/redeem', {
            params: { ...params, path: { instanceId: p.couponId } },
            body: p.body,
          }),
        ),
      };
    case 'stamp':
      return {
        kind: 'stamp',
        result: unwrap(await api.POST('/v1/staff/stamps', { params, body: p.body })),
      };
  }
}

/**
 * Sends a write and settles it. A definite answer (2xx, or a 4xx that
 * says nothing was written) clears the pending action; anything else keeps
 * the key and body so the retry replays the same write.
 */
export async function sendWrite(p: PendingAction): Promise<WriteOutcome> {
  const settle = () => {
    if (getPendingAction()?.key === p.key) setPendingAction(null);
  };
  try {
    const applied = await post(p);
    settle();
    return { ok: true, applied };
  } catch (err) {
    const error = toApiError(err);
    if (isDefinite(error)) {
      settle();
      return { ok: false, error, ambiguous: false };
    }
    setPendingAction({ ...p, error });
    return { ok: false, error, ambiguous: true };
  }
}

/** What a submission came back with. */
export type WriteOutcome =
  { ok: true; applied: Applied } | { ok: false; error: ApiError; ambiguous: boolean };
export type Outcome =
  { ok: true; txn: PointsTransaction } | { ok: false; error: ApiError; ambiguous: boolean };

/** Points writes keep the narrower outcome the forms use. */
export async function sendPointsWrite(p: PendingAction & PointsWrite): Promise<Outcome> {
  const out = await sendWrite(p);
  if (!out.ok) return out;
  return { ok: true, txn: (out.applied as { txn: PointsTransaction }).txn };
}

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
