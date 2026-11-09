import type { components } from './api/schema';

/** The error envelope every MiniLoyal endpoint returns on failure. */
export type ApiError = components['schemas']['Error'];

/**
 * Error codes the SDK adds to the API's own codes:
 * - `network`: the request never reached the API (offline, DNS, CORS).
 * - `redirecting`: the MINI App is being sent to LINE login; stop rendering.
 * - `not_signed_in`: no LIFF ID token is available and auto login is off.
 * - `invalid_response`: the API answered with a body that is not the envelope.
 */
export type SdkErrorCode = 'network' | 'redirecting' | 'not_signed_in' | 'invalid_response';

/** Every failure the SDK throws. `code` is stable; `message` is for humans. */
export class MiniLoyalError extends Error {
  readonly code: string;
  readonly status: number;
  readonly requestId: string | null;
  readonly details: Record<string, unknown> | undefined;

  constructor(
    code: string,
    message: string,
    options: { status?: number; requestId?: string | null; details?: Record<string, unknown> } = {},
  ) {
    super(message);
    this.name = 'MiniLoyalError';
    this.code = code;
    this.status = options.status ?? 0;
    this.requestId = options.requestId ?? null;
    this.details = options.details;
  }

  /** Builds an error from the API's envelope. */
  static fromEnvelope(status: number, body: ApiError): MiniLoyalError {
    return new MiniLoyalError(body.code, body.message, {
      status,
      requestId: body.request_id,
      details: body.details,
    });
  }

  /** Returns the validation message for one field, when the API sent one. */
  field(name: string): string | undefined {
    const v = this.details?.[name];
    return typeof v === 'string' ? v : undefined;
  }
}

/** Narrows an unknown thrown value to a MiniLoyalError. */
export function isMiniLoyalError(err: unknown): err is MiniLoyalError {
  return err instanceof MiniLoyalError;
}
