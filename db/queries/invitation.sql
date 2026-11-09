-- name: CreateInvitation :one
INSERT INTO invitations (id, tenant_id, email, role, token_hash, invited_by_user_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListPendingInvitations :many
SELECT * FROM invitations
WHERE tenant_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
ORDER BY created_at DESC;

-- name: GetInvitation :one
SELECT * FROM invitations WHERE id = $1;

-- name: GetInvitationTenant :one
SELECT tenant_id FROM invitations WHERE id = $1;

-- name: GetInvitationByTokenHash :one
SELECT * FROM invitations
WHERE token_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: AcceptInvitation :execrows
UPDATE invitations SET accepted_at = now(), accepted_by_user_id = $2
WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: RevokeInvitation :execrows
UPDATE invitations SET revoked_at = now()
WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL;
