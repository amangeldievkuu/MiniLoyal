-- name: CreateTenant :one
INSERT INTO tenants (id, type, name, slug, plan, parent_tenant_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTenant :one
SELECT * FROM tenants WHERE id = $1;

-- name: UpdateTenantName :one
UPDATE tenants SET name = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: ListMembershipsForUser :many
SELECT sqlc.embed(t), tu.role
FROM tenant_users tu
JOIN tenants t ON t.id = tu.tenant_id
WHERE tu.user_id = $1
ORDER BY t.created_at;

-- name: GetMembership :one
SELECT * FROM tenant_users WHERE tenant_id = $1 AND user_id = $2;

-- name: InsertMembership :one
INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateMembershipRole :one
UPDATE tenant_users SET role = $3 WHERE tenant_id = $1 AND user_id = $2 RETURNING *;

-- name: DeleteMembership :execrows
DELETE FROM tenant_users WHERE tenant_id = $1 AND user_id = $2;

-- name: CountOwners :one
SELECT count(*) FROM tenant_users WHERE tenant_id = $1 AND role = 'owner';

-- name: ListTeam :many
SELECT tu.user_id, u.email, u.display_name, tu.role, tu.created_at
FROM tenant_users tu
JOIN users u ON u.id = tu.user_id
WHERE tu.tenant_id = $1
ORDER BY tu.created_at;

-- name: GetTeamMember :one
SELECT tu.user_id, u.email, u.display_name, tu.role, tu.created_at
FROM tenant_users tu
JOIN users u ON u.id = tu.user_id
WHERE tu.tenant_id = $1 AND tu.user_id = $2;
