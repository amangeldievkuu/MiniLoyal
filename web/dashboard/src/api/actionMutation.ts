import { useMutation, type UseMutationOptions } from '@tanstack/react-query';
import { useRef } from 'react';

import { newIdempotencyKey } from './client';

/**
 * Like useIdempotentMutation, but the key belongs to the request body: a retry
 * of the same body reuses its key, while an edited body (after a validation
 * error) or another row gets a fresh one, so the server never sees one key with
 * two payloads (422). A success forgets the key.
 */
export function useActionMutation<TData, TVariables>(
  run: (variables: TVariables, idempotencyKey: string) => Promise<TData>,
  options?: Omit<UseMutationOptions<TData, Error, TVariables>, 'mutationFn'>,
) {
  const keysByBody = useRef(new Map<string, string>());
  return useMutation<TData, Error, TVariables>({
    ...options,
    mutationFn: (variables) => {
      const body = JSON.stringify(variables);
      let key = keysByBody.current.get(body);
      if (!key) {
        key = newIdempotencyKey();
        keysByBody.current.set(body, key);
      }
      return run(variables, key);
    },
    onSuccess: (data, variables, context, mutation) => {
      keysByBody.current.delete(JSON.stringify(variables));
      options?.onSuccess?.(data, variables, context, mutation);
    },
  });
}
