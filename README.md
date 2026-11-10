# MiniLoyal

Membership and loyalty backend for **LINE MINI Apps**: LINE login, member cards with rotating QR codes, a points ledger, coupons, stamp cards, push messaging, an admin dashboard, a staff LIFF app and a TypeScript SDK. Multi-tenant, built for agencies and brands in Taiwan first.

Status: **Phase 4 complete** (coupons, stamp cards, outbound events). On top of Phase 3's points ledger, member card and staff app: coupon templates with total and per-member claim limits and an atomic redeem (of concurrent redeems exactly one wins, and one scan redeems at most three coupons), stamp cards with a reward coupon and a 30-minute visit cap, an append-only outbox of outbound events written in the same transaction as each change, dashboard and staff app pages for both, and a separate `miniloyal_worker` database role with only the grants its jobs use. Next: Phase 5 (push messaging, signed outbound webhook delivery, API key scopes and limits). See `docs/plan/implementation-plan.md` for the plan, `docs/adr/` for decisions, `docs/security/` for the review history and `scripts/smoke/` for end-to-end checks.

## Security model in one paragraph

Every tenant-scoped table has forced row-level security. The API connects as `miniloyal_api`, a role that owns nothing and cannot bypass RLS; each request runs inside a transaction that sets the tenant, so a bug in a query cannot read another tenant's rows. The worker connects as `miniloyal_worker`, which owns River's job tables, holds only the grants its jobs use and is also subject to RLS; the API role has no access to River's tables. Pre-tenant lookups (sign-in, API key hash, webhook routing) run in an explicit system mode. LINE channel secrets and TOTP secrets are envelope-encrypted with a key-encryption key from a local file in development or Cloud KMS in production. Passwords are argon2id; refresh tokens and API keys are stored as hashes; every write takes an `Idempotency-Key`.

## Stack

Go 1.27 (stdlib `net/http`), PostgreSQL 17 with `pgx` + `sqlc` + `goose`, OpenAPI 3.0.3 spec-first with `oapi-codegen`, River for background jobs, React 19 + Vite for the dashboard and staff app, TypeScript SDK wrapping `@line/liff`. Deployed with Docker Compose behind nginx in a Taiwan region.

## Quick start

Requirements: Go 1.27, Node 22, Docker.

```sh
cp .env.example .env    # Postgres on 127.0.0.1:5434, role passwords and connection URLs
make dev                # Postgres up, roles and migrations applied, API on :8080
npm install
npm run dev:dashboard   # dashboard on :5173, proxies /v1 to the API
```

`.env` must set `POSTGRES_PASSWORD` (the cluster superuser, used by migrate and backup), `APP_DB_PASSWORD` (`miniloyal_api`) and `WORKER_DB_PASSWORD` (`miniloyal_worker`); docker compose stops with a clear message when one is missing. `.env.example` sets development values. Staging and production refuse those values, whether set directly or inside `DATABASE_URL`, `WORKER_DATABASE_URL` or `MIGRATE_DATABASE_URL`. An existing `.env` from an earlier phase may lack `WORKER_DB_PASSWORD` and `WORKER_DATABASE_URL`; copy them from `.env.example`.

Full stack as deployed (migrate, api, worker, nginx with the dashboard, backups):

```sh
openssl rand -base64 32 > secrets/kek
# Real values for POSTGRES_PASSWORD, APP_DB_PASSWORD, WORKER_DB_PASSWORD and SESSION_SIGNING_KEY in .env
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
