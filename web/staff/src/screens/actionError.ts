import type { TFunction } from 'i18next';

import type { ApiError } from '../api/client';

export interface ActionError {
  message: string;
  /** The scan token no longer works: clear the member and scan again. */
  scanAgain: boolean;
  /** A transient failure: the same submission (same Idempotency-Key) may be retried. */
  retryable: boolean;
}

/** Codes the server sends when the scan token cannot be used (internal/points). */
const SCAN_CODES = new Set(['scan_invalid', 'scan_mismatch']);

/** Maps an earn or redeem failure to what the cashier sees and can do. */
export function describeActionError(err: ApiError, t: TFunction, balance: number): ActionError {
  const fail = (message: string, scanAgain = false, retryable = false): ActionError => ({
    message,
    scanAgain,
    retryable,
  });
  switch (err.code) {
    case 'insufficient_points':
      return fail(
        t('errors.insufficient', {
          balance: (err.detailNumber('balance') ?? balance).toLocaleString(),
        }),
      );
    case 'scan_used':
      return fail(t('errors.scanUsed'), true);
    case 'self_service':
      return fail(t('errors.selfService'));
    case 'points_disabled':
      return fail(t('errors.pointsDisabled'));
    case 'coupon_unavailable':
      return fail(t('errors.couponUnavailable'));
    case 'stamp_visit_limit':
      return fail(t('errors.stampVisitLimit'));
    case 'stamp_card_inactive':
      return fail(t('errors.stampCardInactive'));
    case 'staff_channel_required':
      return fail(t('start.staffChannelRequired'));
    case 'conflict':
      // The same key is still being processed: the answer is not known yet.
      return fail(t('errors.network'), false, true);
  }
  if (SCAN_CODES.has(err.code)) return fail(t('errors.scanAgain'), true);
  if (err.status === 429) {
    return fail(t('errors.rateLimited', { seconds: err.retryAfterSeconds ?? 60 }), false, true);
  }
  if (err.status === 403) return fail(t('errors.notAllowed'));
  if (err.status === 0 || err.status >= 500) return fail(t('errors.network'), false, true);
  if (err.status === 400 && err.message) return fail(err.message);
  return fail(t('errors.generic'));
}
