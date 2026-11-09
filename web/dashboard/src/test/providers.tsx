import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router';
import { render } from '@testing-library/react';
import type { ReactNode } from 'react';
import { vi } from 'vitest';

/**
 * Renders ui under a fresh QueryClient and a memory router, so components that
 * use Link, useNavigate or React Query work. Every path renders the same ui.
 */
export function renderWithProviders(ui: ReactNode, path = '/') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const root = createRootRoute();
  const page = () => <>{ui}</>;
  const index = createRoute({ getParentRoute: () => root, path: '/', component: page });
  const rest = createRoute({ getParentRoute: () => root, path: '$', component: page });
  const router = createRouter({
    routeTree: root.addChildren([index, rest]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

export const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

export interface RecordedCall {
  method: string;
  path: string;
  query: URLSearchParams;
  body: unknown;
  headers: Headers;
}

/**
 * Stubs fetch with a router over method and path, recording every call. The
 * handler returns a Response; unknown routes answer 404.
 */
export function stubFetch(handle: (call: RecordedCall) => Response | undefined): {
  calls: RecordedCall[];
} {
  const calls: RecordedCall[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = input instanceof Request ? input : new Request(input, init);
      const url = new URL(req.url);
      let body: unknown = undefined;
      if (req.method !== 'GET' && req.method !== 'HEAD') {
        const text = await req.text();
        body = text ? (JSON.parse(text) as unknown) : undefined;
      }
      const call: RecordedCall = {
        method: req.method,
        path: url.pathname,
        query: url.searchParams,
        body,
        headers: req.headers,
      };
      calls.push(call);
      return (
        handle(call) ??
        jsonResponse(404, { code: 'not_found', message: 'not found', request_id: 'test' })
      );
    }),
  );
  return { calls };
}
