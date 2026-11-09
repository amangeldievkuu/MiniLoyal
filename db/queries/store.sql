-- name: CreateStore :one
INSERT INTO stores (id, tenant_id, app_id, name, code, address)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetStore :one
SELECT * FROM stores WHERE id = $1;

-- name: ListStores :many
SELECT * FROM stores WHERE app_id = $1 ORDER BY created_at;

-- name: UpdateStore :one
UPDATE stores SET
  name       = coalesce(sqlc.narg('name'), name),
  code       = CASE WHEN sqlc.arg('set_code')::boolean THEN sqlc.narg('code') ELSE code END,
  address    = CASE WHEN sqlc.arg('set_address')::boolean THEN sqlc.narg('address') ELSE address END,
  status     = coalesce(sqlc.narg('status'), status),
  updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteStore :execrows
DELETE FROM stores WHERE id = $1;

-- name: CountStaffInStore :one
SELECT count(*) FROM staff WHERE store_id = $1;
