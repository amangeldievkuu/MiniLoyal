---
paths:
  - 'cmd/**'
  - 'internal/**'
---

# Go backend rules

- Go 1.27, module `github.com/amangeldievkuu/miniloyal`. Standard library first: `net/http` `ServeMux` with method patterns, `log/slog`, `context`, `encoding/json`. Add a dependency only when the standard library cannot do the job.
- Generated code is never edited by hand: `internal/api/**` (oapi-codegen), `internal/db/sqlc/**` (sqlc). Change the source (`api/openapi.yaml`, `db/queries/*.sql`) and run `make gen`.
- Every exported function that touches the database takes `ctx context.Context` first and a principal or explicit `tenantID`/`appID`. No query runs without tenant scope.
- Every write handler reads `Idempotency-Key` and goes through `internal/idempotency`.
- Points and stamps change only by inserting ledger rows inside a transaction with `SELECT ... FOR UPDATE` on the account row.
- Errors: wrap with `fmt.Errorf("...: %w", err)`, define sentinel and typed errors per domain, map to the `Error` envelope in `internal/httpserver/errors.go`. Handlers never panic and never write `err.Error()` to the client for internal errors.
- Logging: `slog` with structured keys (`tenant_id`, `app_id`, `request_id`). Never log tokens, secrets, ID tokens, API keys or raw webhook bodies.
- Time comes from an injected clock (`internal/platform/clock`) so tests are deterministic.
- Tests live next to the code. Integration tests use `internal/testutil` with testcontainers Postgres and guard slow paths with `testing.Short()`.
- `gofmt`, `go vet` and staticcheck must be clean. Run `make lint` before committing.
- Commit messages: short imperative sentences, no prefixes, no trailers.
