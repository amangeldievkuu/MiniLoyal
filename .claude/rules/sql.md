---
paths:
  - 'db/**'
---

# SQL rules

- Migrations: goose, `db/migrations/NNNNN_name.sql`, `-- +goose Up` and `-- +goose Down`, five-digit increasing numbers. Forward-only in production, expand/contract for changes.
- Every tenant table has `tenant_id uuid NOT NULL`; member-scoped tables also have `app_id uuid NOT NULL`. Every table has `created_at timestamptz NOT NULL DEFAULT now()`.
- Every tenant table enables and forces RLS with `CREATE POLICY tenant_isolation ... USING (app_is_system() OR tenant_id = app_tenant_id()) WITH CHECK (app_is_system() OR tenant_id = app_tenant_id())`. `app_tenant_id()` reads the per-transaction setting that `db.WithTenant` sets; `app_is_system()` is true only inside `db.WithSystem`. The application connects as `miniloyal_api` (member of `miniloyal_app`), never as the owner; `migrate up` creates both roles.
- Ledger and event tables are append-only: grant the application role only `SELECT, INSERT` on them.
- Unique constraints encode the invariants: `(app_id, line_user_id)`, `(app_id, idempotency_key)`, `(app_id, line_webhook_event_id)`.
- Indexes lead with `tenant_id` or `app_id`.
- Secrets are `bytea` columns named `*_enc`, envelope-encrypted by the application. No plaintext secrets in any column.
- sqlc queries in `db/queries/<domain>.sql`. Every query filters by `tenant_id` or `app_id`. After editing, run `make gen-sql` and fix all `sqlc vet` output.
- Never hand-edit `internal/db/sqlc/**`.
