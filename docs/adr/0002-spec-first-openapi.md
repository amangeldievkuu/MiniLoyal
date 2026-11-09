# ADR 0002: Spec-first OpenAPI as the contract between agents

**Status:** Accepted, 2026-11-09

## Context

The backend, the React dashboard, the staff app, the TypeScript SDK and the docs are built in
parallel by different Claude Code agents, often in separate git worktrees. They need one
unambiguous contract. The v1 plan said "OpenAPI 3.1 + oapi-codegen". `oapi-codegen` reads
OpenAPI 3.0 documents; 3.1 support in the Go tooling is still partial.

## Decision

- `api/openapi.yaml` is the source of truth for every HTTP endpoint. It is authored as
  **OpenAPI 3.0.3** today. We move to 3.1 when `oapi-codegen` (or its replacement) consumes it
  without a downgrade step.
- Backend: `oapi-codegen` generates types and a **strict server** for `std-http` into
  `internal/api/gen/`. Handlers implement the generated interface; nothing else parses
  requests.
- Clients: `openapi-typescript` generates types and `openapi-fetch` provides the typed client
  for `web/dashboard`, `web/staff` and `sdk/js`. The SDK re-exports a friendlier facade.
- Docs: Scalar renders the same document.
- Workflow, enforced by the `add-endpoint` skill: edit the spec → `make gen` → implement the
  handler and service → write tests → update docs text in the spec itself.
- Every write operation declares the `Idempotency-Key` header parameter as required.
- Generated files are committed so worktrees and CI never diverge on generator versions.

## Consequences

- Frontend and SDK agents can start from the spec before the backend exists, using mocks.
- Spec changes are visible in review as the only way to change the API surface.
- Two generators to keep pinned (`oapi-codegen`, `openapi-typescript`); `make gen` and a CI
  check that regenerated output matches the committed files catch drift.
- 3.0.3 lacks a few 3.1 niceties (JSON Schema 2020-12, `webhooks`). Outbound webhook payloads
  are documented as ordinary schemas under `components` until the move to 3.1.
