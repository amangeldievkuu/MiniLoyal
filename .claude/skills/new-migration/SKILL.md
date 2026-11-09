---
name: new-migration
description: Create a goose migration with the MiniLoyal tenant, RLS and append-only conventions, regenerate sqlc, and add the cross-tenant test. Use for any schema change.
argument-hint: '[short_snake_case_name] [what changes]'
---

# New migration

## Conventions

- File: `db/migrations/NNNNN_<name>.sql`, five digits, next number after the highest existing one.
- Sections: `-- +goose Up` then `-- +goose Down`. Wrap multi-statement functions or `DO` blocks in `-- +goose StatementBegin` / `-- +goose StatementEnd`.
- Every tenant table has `tenant_id uuid NOT NULL`. Member-scoped tables also have `app_id uuid NOT NULL`.
- Every table has `id`, `created_at timestamptz NOT NULL DEFAULT now()`, and `updated_at` when rows change.
- Enable and force RLS on every tenant table. Add the tenant policy.
- Every table gets explicit grants to `miniloyal_app`; ledger and event tables are append-only, so grant only `SELECT, INSERT`.
- Indexes start with `tenant_id` or `app_id`.
- Secrets are `bytea` columns named `*_enc` holding envelope-encrypted data.

## Template

```sql
-- +goose Up
-- Convention: every row carries tenant_id (and app_id for member-scoped data); RLS enforces it.
CREATE TABLE stamp_events (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  app_id      uuid NOT NULL,
  member_id   uuid NOT NULL REFERENCES members(id),
  stamp_card_id uuid NOT NULL REFERENCES stamp_cards(id),
  store_id    uuid REFERENCES stores(id),
  staff_id    uuid REFERENCES staff(id),
  delta       integer NOT NULL,
  idempotency_key text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, idempotency_key)
);
CREATE INDEX stamp_events_member_idx ON stamp_events (app_id, member_id, created_at DESC);

ALTER TABLE stamp_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE stamp_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON stamp_events
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());

GRANT SELECT, INSERT ON stamp_events TO miniloyal_app; -- append-only: no UPDATE or DELETE grant

-- +goose Down
DROP TABLE stamp_events;
```

## Steps

1. Pick the next number and create the file from the template.
2. Write `Up` and `Down`. Keep production changes expand/contract: add first, backfill, switch, remove in a later release.
3. Start the database and apply: `make db-up && make migrate`. Then test the rollback once: `go run ./cmd/migrate down` followed by `make migrate` again.
4. Add or update sqlc queries in `db/queries/<domain>.sql`. Every query names `tenant_id` or `app_id` in `WHERE`.
5. Run `make gen-sql`. Fix every `sqlc vet` message.
6. Add a cross-tenant test in `db/migrations/rls_test.go` (package `migrations_test`, helpers from `internal/testutil`): insert a row as tenant A, set `app.tenant_id` to tenant B, assert zero rows and that an insert with tenant A's id fails the `WITH CHECK`.
7. Run `make test-short`.
8. Commit: `Add stamp_events table`.

## Report

- Migration file and tables or columns.
- Generated query functions and signatures.
- Anything the backend must do (set `app.tenant_id`, pass `app_id`).
