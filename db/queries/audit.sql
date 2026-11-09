-- name: InsertAuditEntry :one
INSERT INTO audit_log (tenant_id, app_id, actor_type, actor_id, action, target_type, target_id, metadata, request_id, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, created_at;

-- name: ListAuditLog :many
SELECT * FROM audit_log
WHERE tenant_id = sqlc.arg('tenant_id')
  AND (sqlc.narg('app_id')::uuid IS NULL OR app_id = sqlc.narg('app_id')::uuid)
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_limit');
