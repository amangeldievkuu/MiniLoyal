---
name: add-endpoint
description: Add or change a MiniLoyal API endpoint spec-first. Use when a new route, request field, response or error is needed.
argument-hint: '[METHOD /v1/path] [one-line purpose]'
---

# Add an endpoint, spec first

The OpenAPI document is the contract between the backend, the dashboard, the staff app and the SDK. Code follows the spec, never the other way round.

## Steps

1. **Design in `api/openapi.yaml`.**
   - Add the path and operation. Set `operationId` in `camelCase` (for example `issueCoupon`).
   - Choose the security scheme: `memberSession`, `staffSession`, `dashboardSession` or `apiKey`. One operation, one or more schemes, listed explicitly.
   - Path parameters are IDs scoped to the principal. Never add `tenant_id` or `app_id` to the request body; they come from the principal or the path.
   - For every write, add the `Idempotency-Key` header parameter by reference (`$ref: '#/components/parameters/IdempotencyKey'`) and document the 409 and 422 responses.
   - Reuse components. Add new schemas under `components/schemas` with `description` on every field.
   - Error responses use `#/components/schemas/Error`.
   - Write `summary` and `description` now. `docs-writer` polishes later.

2. **Generate.** Run `make gen-api`. This regenerates `internal/api` (Go strict server) and the TypeScript schemas for `web/dashboard`, `web/staff` and `sdk/js`. The build fails until the new strict-server method exists.

3. **Data access.** If the operation needs a query, add it to `db/queries/<domain>.sql`. Every query filters by `tenant_id` or `app_id`. Run `make gen-sql`. If a table or column is missing, stop and run the `new-migration` skill first.

4. **Service.** Implement the use case in `internal/<domain>/service.go`. It takes a principal and typed inputs, opens a transaction when it writes, and returns typed domain errors.

5. **Handler.** Implement the generated strict interface method in `internal/httpserver/<domain>.go`. Decode, call the service, map errors with the mapper in `internal/httpserver/errors.go`, encode. No business logic in handlers.

6. **Tests.**
   - Unit tests for the service.
   - An integration test in `internal/httpserver` using `testutil.NewServer(t)`: happy path, validation error, cross-tenant 404, and idempotent replay for writes.
   - Add the operation to the cross-tenant table if it is not auto-discovered.

7. **Verify.** `make test-short` and `make lint` pass. `make gen` produces no diff.

8. **Document.** Update the guide under `docs/` if the operation changes a developer-facing flow. Update `sdk/js` if the operation is member-facing (ask `sdk-ts`).

9. **Commit.** Short imperative message, for example `Add coupon issue endpoint`.

## Checklist before you report

- [ ] `operationId`, security, `Idempotency-Key`, `Error` responses present in the spec
- [ ] `make gen` clean
- [ ] Query filters by tenant or app
- [ ] Cross-tenant and idempotency tests exist
- [ ] Audit log row written for admin or staff actions
