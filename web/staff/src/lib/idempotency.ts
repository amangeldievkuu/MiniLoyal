import { useCallback, useRef, useState } from 'react';

import { toApiError, type ApiError } from '../api/client';

/**
 * Keeps one Idempotency-Key per logical submission: a retry after a failure
 * reuses it, a success or a change to the request mints a new one.
 */
export function createIdempotencyKeyHolder() {
  let current: string | null = null;
  return {
    key(): string {
      current ??= crypto.randomUUID();
      return current;
    },
    reset() {
      current = null;
    },
  };
}

/**
 * Runs a write with an Idempotency-Key, ignoring taps while one is in flight
 * (a double tap at the counter must not send two requests).
 */
export function useIdempotentSubmit<T>(run: (idempotencyKey: string) => Promise<T>) {
  const holder = useRef(createIdempotencyKeyHolder());
  const inFlight = useRef(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const submit = useCallback(async (): Promise<T | null> => {
    if (inFlight.current) return null;
    inFlight.current = true;
    setPending(true);
    setError(null);
    try {
      const result = await run(holder.current.key());
      holder.current.reset();
      return result;
    } catch (err) {
      setError(toApiError(err));
      return null;
    } finally {
      inFlight.current = false;
      setPending(false);
    }
  }, [run]);

  /** Call when the request changes: a different payload needs a new key. */
  const invalidateKey = useCallback(() => {
    holder.current.reset();
    setError(null);
  }, []);

  return { submit, pending, error, invalidateKey };
}
