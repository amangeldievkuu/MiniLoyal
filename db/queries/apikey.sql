-- name: CreateApiKey :one
INSERT INTO api_keys (id, tenant_id, app_id, name, prefix, key_hash, scopes, created_by_user_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetApiKey :one
SELECT * FROM api_keys WHERE id = $1;

-- name: GetApiKeyByHash :one
SELECT * FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: ListApiKeys :many
SELECT * FROM api_keys WHERE app_id = $1 AND revoked_at IS NULL ORDER BY created_at;

-- name: RevokeApiKey :execrows
UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: TouchApiKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;

-- name: GetApiKeyTenant :one
SELECT tenant_id FROM api_keys WHERE id = $1;
