# MiniLoyal

Membership and loyalty backend for **LINE MINI Apps**: LINE login, member cards with rotating QR codes, a points ledger, coupons, stamp cards, push messaging, an admin dashboard, a staff LIFF app and a TypeScript SDK. Multi-tenant, built for agencies and brands in Taiwan first.

Status: **Phase 0** (scaffold and tooling). See `docs/plan/implementation-plan.md` for the plan and `docs/adr/` for decisions.

## Stack

Go 1.27 (stdlib `net/http`), PostgreSQL 17 with `pgx` + `sqlc` + `goose`, OpenAPI 3.0.3 spec-first with `oapi-codegen`, River for background jobs, React 19 + Vite for the dashboard and staff app, TypeScript SDK wrapping `@line/liff`. Deployed with Docker Compose behind nginx in a Taiwan region.

## Quick start

Requirements: Go 1.27, Node 22, Docker.

```sh
cp .env.example .env
make dev            # Postgres up, migrations applied, API on :8080
npm install
npm run dev:dashboard   # dashboard on :5173, proxies /v1 to the API
```

Useful targets: `make test`, `make lint`, `make gen` (regenerate server, queries and TS types after editing `api/openapi.yaml` or `db/`), `make ci`. Run `make help` for the list.

## Layout

| Path                                   | What                                      |
| -------------------------------------- | ----------------------------------------- |
| `cmd/api`, `cmd/worker`, `cmd/migrate` | The three binaries                        |
| `internal/httpserver`                  | Routes, middleware, strict handlers       |
| `internal/api`, `internal/db/sqlc`     | Generated code (do not edit)              |
| `internal/platform/*`                  | Config, DB pool, logging, tracing, crypto |
| `api/openapi.yaml`                     | The API contract                          |
| `db/migrations`, `db/queries`          | goose migrations, sqlc queries            |
| `web/dashboard`, `web/staff`           | React apps                                |
| `sdk/js`                               | `@miniloyal/sdk`                          |
| `apps/demo-cafe`                       | Demo MINI App built on the SDK            |
| `deploy/`                              | nginx, backup and restore scripts         |
| `docs/`                                | Plan, ADRs, LINE platform notes           |

## Development with Claude Code

The repo ships a Claude Code harness in `.claude/`: specialised subagents, skills (`/add-endpoint`, `/new-migration`, `/line-api`, `/verify`), path-scoped rules, hooks that format and vet on edit, and a `build-phase` workflow for parallel feature work. `CLAUDE.md` holds the conventions and invariants.
