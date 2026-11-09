# MiniLoyal

Membership and loyalty backend for **LINE MINI Apps**: LINE login, member cards with rotating QR codes, a points ledger, coupons, stamp cards, push messaging, an admin dashboard, a staff LIFF app and a TypeScript SDK. Multi-tenant, built for agencies and brands in Taiwan first.

Status: **Phase 1 complete** (platform core). Working today: dashboard accounts with TOTP and rotating refresh sessions, tenants and teams with roles, apps with encrypted LINE channel credentials, stores, staff with invite codes, scoped API keys, an append-only audit log, idempotent writes, and row-level tenant isolation enforced by PostgreSQL. Next: Phase 2 (LINE login, members, inbound webhooks). See `docs/plan/implementation-plan.md` for the plan and `docs/adr/` for decisions.

## Security model in one paragraph

Every tenant-scoped table has forced row-level security. The API connects as `miniloyal_api`, a role that owns nothing and cannot bypass RLS; each request runs inside a transaction that sets the tenant, so a bug in a query cannot read another tenant's rows. Pre-tenant lookups (sign-in, API key hash, webhook routing) run in an explicit system mode. LINE channel secrets and TOTP secrets are envelope-encrypted with a key-encryption key from a local file in development or Cloud KMS in production. Passwords are argon2id; refresh tokens and API keys are stored as hashes; every write takes an `Idempotency-Key`.

## Stack

Go 1.27 (stdlib `net/http`), PostgreSQL 17 with `pgx` + `sqlc` + `goose`, OpenAPI 3.0.3 spec-first with `oapi-codegen`, River for background jobs, React 19 + Vite for the dashboard and staff app, TypeScript SDK wrapping `@line/liff`. Deployed with Docker Compose behind nginx in a Taiwan region.

## Quick start

Requirements: Go 1.27, Node 22, Docker.

```sh
cp .env.example .env    # Postgres on 127.0.0.1:5434, app role and owner URLs
make dev                # Postgres up, roles and migrations applied, API on :8080
npm install
npm run dev:dashboard   # dashboard on :5173, proxies /v1 to the API
```

Full stack as deployed (migrate, api, worker, nginx with the dashboard, backups):

```sh
openssl rand -base64 32 > secrets/kek
KEK_FILE=/secrets/kek docker compose --profile app up -d --build   # http://localhost:8080
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
