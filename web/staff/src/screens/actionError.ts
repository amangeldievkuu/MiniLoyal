import type { TFunction } from 'i18next';

import type { ApiError } from '../api/client';

export interface ActionError {
  message: string;
  /** The scan token no longer works: clear the member and scan again. */
  scanAgain: boolean;
  /** A transient failure: the same submission (same Idempotency-Key) may be retried. */
  retryable: boolean;
}

const SCAN_CODES = new Set(['scan_mismatch', 'scan_expired', 'invalid_scan_token']);

/** Maps an earn or redeem failure to what the cashier sees and can do. */
export function describeActionError(err: ApiError, t: TFunction, balance: number): ActionError {
  if (err.status === 409 || err.code === 'insufficient_points') {
    return {
      message: t('errors.insufficient', {
        balance: (err.detailNumber('balance') ?? balance).toLocaleString(),
      }),
      scanAgain: false,
      retryable: false,
    };
  }
  if (err.status === 403 || SCAN_CODES.has(err.code)) {
    return { message: t('errors.scanAgain'), scanAgain: true, retryable: false };
  }
  if (err.status === 0 || err.status >= 500) {
    return { message: t('errors.network'), scanAgain: false, retryable: true };
  }
  if (err.status === 400 && err.message) {
    return { message: err.message, scanAgain: false, retryable: false };
  }
  return { message: t('errors.generic'), scanAgain: false, retryable: false };
}
