-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (id, user_id, token_hash, family, parent_id, expires_at, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens WHERE token_hash = $1;

-- name: RevokeRefreshToken :execrows
-- revoked_at comes from the service clock so the lost-response window compares like with like.
UPDATE refresh_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshTokenByHash :execrows
UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshTokenFamily :exec
UPDATE refresh_tokens SET revoked_at = now() WHERE family = $1 AND revoked_at IS NULL;

-- name: RevokeUserRefreshTokens :exec
UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: ListRefreshTokenChildren :many
SELECT id, revoked_at, created_at FROM refresh_tokens WHERE parent_id = $1;

-- name: DeleteSpentRefreshTokens :execrows
DELETE FROM refresh_tokens
WHERE expires_at < now() OR revoked_at < now() - interval '60 days';

-- name: DeleteSpentRefreshTokensForUser :execrows
DELETE FROM refresh_tokens
WHERE user_id = $1 AND (expires_at < now() OR revoked_at < now() - interval '60 days');
