---
paths:
  - 'web/**'
  - 'apps/**'
---

# Frontend rules

- React 19, Vite, TypeScript strict. `web/dashboard` uses TanStack Router and Query, Tailwind and shadcn/ui. `web/staff` and `apps/demo-cafe` use `@line/liff`.
- API types come from the generated `src/api/schema.d.ts` (`make gen-api`) through `openapi-fetch`. No hand-written API types, no `any` at the boundary, no direct `fetch` outside the API client module.
- i18n with i18next. Every visible string uses `t()` with keys in `src/locales/zh-TW.json` and `src/locales/en.json`. zh-TW is primary. A missing key fails `npm run check`.
- Tokens live in memory. Refresh tokens are httpOnly cookies set by the API. Never use localStorage or sessionStorage for tokens.
- Every mutation sends an `Idempotency-Key` generated once per submission and reused on retry.
- LIFF IDs and API base URLs come from `VITE_*` environment variables, one set per environment. Never hard-code them.
- Accessibility: labelled inputs, keyboard reachable, visible focus, `aria-live` for async state. Layout works at 360 px width with a 16 px gutter and no horizontal scroll.
- Tests: Vitest for hooks and validation, msw for API mocks, Playwright smoke tests under `e2e/`.
- Before committing: `npm run lint`, `npm run check`, `npm test`, `npm run build` in the package you changed.
