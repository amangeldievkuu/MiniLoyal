import { useMutation, type UseMutationOptions } from '@tanstack/react-query';
import { useRef } from 'react';

import { createIdempotencyKeyHolder } from './client';

/**
 * useMutation whose function receives an Idempotency-Key that stays the same
 * across retries of one submission and changes after a success.
 */
export function useIdempotentMutation<TData, TVariables>(
  run: (variables: TVariables, idempotencyKey: string) => Promise<TData>,
  options?: Omit<UseMutationOptions<TData, Error, TVariables>, 'mutationFn'>,
) {
  const holder = useRef(createIdempotencyKeyHolder());
  return useMutation<TData, Error, TVariables>({
    ...options,
    mutationFn: (variables) => run(variables, holder.current.key()),
    onSuccess: (data, variables, context, mutation) => {
      holder.current.reset();
      options?.onSuccess?.(data, variables, context, mutation);
    },
  });
}
