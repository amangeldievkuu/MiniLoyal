-- name: CreateApp :one
INSERT INTO apps (id, tenant_id, name, slug, locale)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetApp :one
SELECT * FROM apps WHERE id = $1;

-- name: ListApps :many
SELECT * FROM apps WHERE tenant_id = $1 ORDER BY created_at;

-- name: CountApps :one
SELECT count(*) FROM apps WHERE tenant_id = $1;

-- name: UpdateApp :one
UPDATE apps SET
  name       = coalesce(sqlc.narg('name'), name),
  locale     = coalesce(sqlc.narg('locale'), locale),
  status     = coalesce(sqlc.narg('status'), status),
  updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: UpdateAppOA :one
UPDATE apps SET
  oa_channel_id         = $2,
  oa_basic_id           = $3,
  oa_channel_secret_enc = $4,
  updated_at            = now()
WHERE id = $1
RETURNING *;

-- name: ListAppChannels :many
SELECT * FROM app_channels
WHERE app_id = $1
ORDER BY CASE environment WHEN 'developing' THEN 0 WHEN 'review' THEN 1 ELSE 2 END;

-- name: UpsertAppChannel :one
INSERT INTO app_channels (id, tenant_id, app_id, environment, channel_id, liff_id, channel_secret_enc, verified_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (app_id, environment) DO UPDATE SET
  channel_id         = EXCLUDED.channel_id,
  liff_id            = EXCLUDED.liff_id,
  channel_secret_enc = coalesce(EXCLUDED.channel_secret_enc, app_channels.channel_secret_enc),
  verified_at        = EXCLUDED.verified_at,
  updated_at         = now()
RETURNING *;

-- name: DeleteAppChannel :execrows
DELETE FROM app_channels WHERE app_id = $1 AND environment = $2;

-- name: GetAppChannelsByChannelID :many
SELECT * FROM app_channels WHERE channel_id = $1;

-- name: GetAppTenant :one
SELECT tenant_id FROM apps WHERE id = $1;

-- name: SetAppOAVerified :exec
UPDATE apps SET oa_verified_at = now(), updated_at = now() WHERE id = $1;

-- name: ClearAppOAVerified :exec
UPDATE apps SET oa_verified_at = NULL, updated_at = now() WHERE id = $1;

-- name: SetAppChannelVerified :exec
UPDATE app_channels SET verified_at = now(), updated_at = now() WHERE id = $1;

-- name: ListAppChannelsForLogin :many
-- Every configured channel ID of the app is an acceptable ID token audience.
SELECT id, environment, channel_id, liff_id, channel_secret_enc, verified_at
FROM app_channels WHERE app_id = $1;
