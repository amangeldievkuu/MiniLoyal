-- Expiry comparisons take "now" from the service clock so tests with a frozen
-- clock stay deterministic.

-- name: CreateInvitation :one
INSERT INTO invitations (id, tenant_id, email, role, token_hash, invited_by_user_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ExpirePendingInvitation :exec
-- Marks an expired pending invitation for the email revoked so a new one can
-- be created (the pending uniqueness index ignores revoked rows).
UPDATE invitations SET revoked_at = sqlc.arg('now')
WHERE tenant_id = $1 AND email = $2 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at <= sqlc.arg('now');

-- name: ListPendingInvitations :many
SELECT * FROM invitations
WHERE tenant_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > sqlc.arg('now')
ORDER BY created_at DESC;

-- name: GetInvitation :one
SELECT * FROM invitations WHERE id = $1;

-- name: GetInvitationTenant :one
SELECT tenant_id FROM invitations WHERE id = $1;

-- name: GetInvitationByTokenHash :one
SELECT * FROM invitations
WHERE token_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > sqlc.arg('now');

-- name: AcceptInvitation :execrows
UPDATE invitations SET accepted_at = sqlc.arg('now'), accepted_by_user_id = $2
WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > sqlc.arg('now');

-- name: RevokeInvitation :execrows
UPDATE invitations SET revoked_at = sqlc.arg('now')
WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL;
