-- name: InsertSecurityEvent :exec
INSERT INTO security_events (user_id, event, ip, user_agent, metadata)
VALUES ($1, $2, $3, $4, $5);

-- name: ListSecurityEvents :many
SELECT * FROM security_events
WHERE user_id = sqlc.arg('user_id')
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_limit');
