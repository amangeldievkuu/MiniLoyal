-- Outbound events: the append-only outbox. Written in the same transaction
-- as the change it describes; delivery is Phase 5.

-- name: InsertOutboundEvent :one
INSERT INTO outbound_events (tenant_id, app_id, type, member_id, payload, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

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
