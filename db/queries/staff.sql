-- name: CreateStaff :one
INSERT INTO staff (id, tenant_id, app_id, store_id, display_name, role, invite_code_hash, invite_expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetStaff :one
SELECT * FROM staff WHERE id = $1;

-- name: ListStaff :many
SELECT * FROM staff WHERE app_id = $1 ORDER BY created_at;

-- name: UpdateStaff :one
UPDATE staff SET
  display_name = coalesce(sqlc.narg('display_name'), display_name),
  store_id     = CASE WHEN sqlc.arg('set_store_id')::boolean THEN sqlc.narg('store_id') ELSE store_id END,
  role         = coalesce(sqlc.narg('role'), role),
  status       = coalesce(sqlc.narg('status'), status),
  updated_at   = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteStaff :execrows
DELETE FROM staff WHERE id = $1;

-- name: GetStaffTenant :one
SELECT tenant_id FROM staff WHERE id = $1;

-- name: GetStaffByLineUserID :one
SELECT * FROM staff WHERE app_id = $1 AND line_user_id = $2;

-- name: GetStaffByInviteHash :one
-- Invite codes are matched within one app only, and only while unexpired.
SELECT * FROM staff
WHERE app_id = $1 AND invite_code_hash = $2 AND line_user_id IS NULL
  AND invite_expires_at > sqlc.arg('now');

-- name: BindStaffLineUser :execrows
UPDATE staff SET line_user_id = $2, invite_code_hash = NULL, invite_expires_at = NULL, updated_at = now()
WHERE id = $1 AND line_user_id IS NULL;

-- name: TouchStaffLogin :exec
UPDATE staff SET last_login_at = now() WHERE id = $1;

-- name: EndStaffSessions :exec
UPDATE staff SET sessions_valid_from = $2, updated_at = now() WHERE id = $1;

-- name: ResetStaffInvite :one
-- A new invite code unbinds the LINE account (lost phone, new person).
UPDATE staff SET line_user_id = NULL, invite_code_hash = $2, invite_expires_at = $3,
  sessions_valid_from = $4, updated_at = now()
WHERE id = $1
RETURNING *;
