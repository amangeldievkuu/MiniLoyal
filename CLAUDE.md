# MiniLoyal

Multi-tenant membership and loyalty backend for LINE MINI Apps, Taiwan first, sold to agencies and brands: LINE login, member cards with rotating QR, points ledger, coupons, stamp cards, push messaging, admin dashboard, staff LIFF app, TypeScript SDK. Go API plus React front ends.

Read first when relevant: `docs/plan/implementation-plan.md` (what we build and in which phase), `docs/adr/` (decisions), `docs/line/platform-notes.md` and the `line-api` skill (verified LINE facts). Rules per area load from `.claude/rules/`.

## Commands

- `make dev`: Postgres via compose (port 5434), migrations, API on :8080 using `.env`
- `make test`, `make test-short`, `make lint`, `make vuln`, `make ci` (what CI runs)
- `make gen`: regenerate `internal/api/api.gen.go` (oapi-codegen), `internal/db/sqlc` (sqlc) and `*/src/api/schema.d.ts` (openapi-typescript). Generated code is committed; never hand-edit it.
- `make migrate`, `make migrate-status`, `go run ./cmd/migrate create NAME`, `go run ./cmd/migrate down`
- `npm run dev:dashboard` (Vite :5173, proxies `/v1` to :8080), `npm run dev:staff`, `npm run dev:demo`
- `docker compose up -d db`; full stack `docker compose --profile app up -d --build`
- Tools run as `go run pkg@version` from the Makefile; nothing to install beyond Go 1.27, Node 22, Docker.

## Layout

- `cmd/{api,worker,migrate}`: binaries. `api -healthcheck` is the container probe.
- `internal/httpserver`: routes, middleware, strict handlers implementing `api.StrictServerInterface`; `errors.go` maps errors to the `Error` envelope.
- `internal/api` (generated), `internal/db/sqlc` (generated).
- `internal/platform/{config,db,log,otel,clock,crypto}`: shared infrastructure.
- Feature packages go in `internal/<domain>`: tenant, lineauth, line, member, points, coupon, stamp, staff, messaging, webhook, apikey, audit, idempotency, jobs.
- `db/migrations` (goose, embedded), `db/queries` (sqlc), `api/openapi.yaml` (the contract, OpenAPI 3.0.3).
- `web/dashboard`, `web/staff` (React 19 + Vite), `sdk/js` (`@miniloyal/sdk`), `apps/demo-cafe`.
- `deploy/` (nginx, backup, restore), `docs/`, `.claude/` (agents, skills, rules, hooks, workflows).

## Workflow: spec first

1. Edit `api/openapi.yaml`. Every operation has an `operationId`; every write references `#/components/parameters/IdempotencyKey`; errors use `#/components/schemas/Error`.
2. `make gen-api`, then implement the generated strict method in `internal/httpserver`, delegating to a service in `internal/<domain>`.
3. Schema change: `/new-migration` (goose file, RLS policy, sqlc query, `make gen-sql`, cross-tenant test).
4. Tests next to the code. Integration tests need `TEST_DATABASE_URL` and skip without it.
5. `make ci` before handing off. Skills: `/add-endpoint`, `/new-migration`, `/line-api`, `/verify`.

## Invariants

- Tenant isolation: every table carries `tenant_id` (and `app_id` for member-scoped data); RLS enabled and forced; the app connects as `miniloyal_app`, never the table owner; every query filters by tenant or app; every endpoint has a cross-tenant test.
- Idempotency: every POST, PUT and PATCH requires `Idempotency-Key`; same key and payload replays the stored response; same key with a different payload returns 422. DELETE is the documented exception (a repeat is a 404). Authorization runs before a replay.
- Security review findings and their status live in `docs/security/`; read the latest before touching auth, RLS or crypto.
- Points and stamps are append-only ledgers. A balance changes inside one transaction after `SELECT ... FOR UPDATE` on the account row; points lots are consumed FIFO; corrections are new rows.
- LINE user IDs are per provider. Members are unique on `(app_id, line_user_id)`. A MINI App channel and the brand's OA channel must share one provider.
- A MINI App has three internal channels (Developing, Review, Published) with different LIFF IDs; accept each configured channel ID as a valid `aud`.
- Secrets: channel secrets are envelope-encrypted at rest; API keys and refresh tokens are stored as hashes; never log tokens, secrets, ID tokens, QR payloads or raw webhook bodies.
- Time comes from `internal/platform/clock` in services, so tests are deterministic.
- Sessions: members get a 1-hour JWT and the SDK re-exchanges the LIFF ID token; dashboard users get a 15-minute JWT plus rotating hashed refresh tokens in httpOnly cookies.

## Style

- Go: standard library first (`net/http` ServeMux with method patterns, `log/slog`, `context` first argument), pgx without an ORM, errors wrapped with `%w`, table-driven tests, gofmt and staticcheck clean. Small packages named after the domain.
- TypeScript: strict, `import type`, generated API types only, React function components, Prettier (single quotes, width 100), ESLint strict.
- Docs and comments: short sentences, say why not what, no em dashes.

## Agents and parallel work

Subagents live in `.claude/agents`: backend-go, db-migrations, line-integration, frontend-dashboard, staff-app, sdk-ts, test-engineer, security-reviewer (read-only), docs-writer, integrator. Feature agents work in git worktrees on their own branch; only integrator merges to `main`, after `make ci` passes. From Phase 2, independent modules can run through the `build-phase` workflow when the user asks for a workflow.

## Git

Short imperative commit messages, no prefixes, no trailers. Commit generated code with the change that caused it. Never force-push `main`. Migrations are a single increasing sequence.
