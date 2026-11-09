---
paths:
  - 'sdk/**'
---

# SDK rules

- `@miniloyal/sdk` is framework-agnostic TypeScript. ESM and CJS via `tsup`, declarations shipped, `@line/liff` as a peer dependency. No UI framework inside the SDK.
- Public types are re-exported from the generated `src/api/schema.d.ts`. Never duplicate API shapes by hand.
- `MiniLoyal.init({ appId, liffId })` handles `liff.init`, login, ID token exchange at `POST /v1/auth/line`, and forwards `liff.getAccessToken()` as `liff_access_token`.
- The session token stays in memory. On 401, re-exchange the ID token once and retry. Nothing is persisted.
- Every mutation accepts `idempotencyKey` and generates one when absent.
- Errors are `MiniLoyalError` with `code`, `status` and `requestId`. Raw fetch errors never leak.
- Bundle budget: under 15 kB gzipped excluding `@line/liff`. The build fails when exceeded.
- README examples live in `sdk/js/examples/` and are type-checked in CI.
- Semantic versioning. Breaking changes are listed in `CHANGELOG.md` under the SDK heading.
