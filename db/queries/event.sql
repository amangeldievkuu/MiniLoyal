-- Outbound events: the append-only outbox. Written in the same transaction
-- as the change it describes; delivery is Phase 5.

-- name: InsertOutboundEvent :exec
-- No RETURNING: the worker role may insert events but not read them.
INSERT INTO outbound_events (tenant_id, app_id, type, member_id, payload, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListOutboundEvents :many
-- An app's events, newest first; type NULL lists every type. Keyset on the last id.
SELECT * FROM outbound_events
WHERE app_id = sqlc.arg('app_id')
  AND (sqlc.narg('type')::text IS NULL OR type = sqlc.narg('type')::text)
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_limit');

-- name: ListMemberOutboundEvents :many
-- One member's events, oldest first (PDPA export).
SELECT * FROM outbound_events
WHERE app_id = $1 AND member_id = $2
ORDER BY id;

-- name: InsertOutboundEvents :exec
-- Many events of one app in one statement (a webhook delivery creating
-- many members), in the caller's transaction. The arrays line up by index:
-- set-returning functions in one select list advance together.
INSERT INTO outbound_events (tenant_id, app_id, type, member_id, payload, occurred_at)
SELECT sqlc.arg('tenant_id')::uuid, sqlc.arg('app_id')::uuid,
  unnest(sqlc.arg('types')::text[]),
  unnest(sqlc.arg('member_ids')::uuid[]),
  unnest(sqlc.arg('payloads')::jsonb[]),
  unnest(sqlc.arg('occurred_ats')::timestamptz[]);
