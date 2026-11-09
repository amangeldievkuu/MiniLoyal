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
