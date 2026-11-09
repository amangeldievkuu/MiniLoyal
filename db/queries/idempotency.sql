-- name: InsertIdempotencyKey :execrows
INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE tenant_id = $1 AND scope = $2 AND key = $3;

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys SET status_code = $4, response_body = $5
WHERE tenant_id = $1 AND scope = $2 AND key = $3;

-- name: DeleteIdempotencyKey :exec
DELETE FROM idempotency_keys WHERE tenant_id = $1 AND scope = $2 AND key = $3;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at < now();

-- name: ClaimIdempotencyKey :one
-- A locking upsert: a concurrent duplicate blocks on the row until the first
-- request commits, then sees its stored outcome. inserted is true only for the
-- row this transaction created (created_at defaults to this transaction's now()).
INSERT INTO idempotency_keys (tenant_id, scope, key, request_hash, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, scope, key) DO UPDATE SET key = EXCLUDED.key
RETURNING (created_at = now())::boolean AS inserted, request_hash, status_code, response_body;

-- name: ExpireIdempotencyKey :exec
-- Drops a stale outcome so the claim that follows starts fresh.
DELETE FROM idempotency_keys
WHERE tenant_id = $1 AND scope = $2 AND key = $3 AND expires_at <= sqlc.arg('now');

-- name: DeleteIdempotencyScope :exec
-- Removes every stored outcome of one principal (used when a member is erased).
DELETE FROM idempotency_keys WHERE tenant_id = $1 AND scope = $2;
