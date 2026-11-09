# ADR 0001: Go 1.27 with the standard-library HTTP mux

**Status:** Accepted, 2026-11-09

## Context

MiniLoyal is a multi-tenant HTTP API with a background worker. The team already writes Go
services on `net/http` with method-pattern routing (`"GET /n/{id}"`),
`http.NewCrossOriginProtection`, `log/slog` and graceful shutdown through
`signal.NotifyContext`. The v1 plan left the router open between `net/http` and `chi`.

The API contract is generated from OpenAPI (ADR 0002), so routing is mostly produced by the
code generator. Middleware needs are small: request logging, auth, tenant context, rate
limits, idempotency.

## Decision

- Go 1.27, pinned in `go.mod` with `go 1.27`; CI uses the latest stable toolchain so builds
  pick up standard-library security fixes.
- Builds are `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`. `pgx` is pure Go, so no cgo is needed.
- Routing uses `http.NewServeMux` with method patterns. `oapi-codegen` generates the strict
  server for `std-http`. Middleware is plain `func(http.Handler) http.Handler`, composed in one
  place in `cmd/api`.
- Logging is `log/slog` with a JSON handler in production and a text handler in development.
- No web framework. `chi` can be introduced later for route groups if the middleware stack
  outgrows a hand-written chain; the generated server does not depend on it.

## Consequences

- One fewer dependency and no framework-specific idioms for agents to learn.
- Route groups are expressed by prefix helpers rather than nested routers; a small amount of
  boilerplate in `cmd/api`.
- The generated server interface is the contract. Handlers are thin adapters over services in
  `internal/*`, which keeps business logic testable without HTTP.
