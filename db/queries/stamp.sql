-- Stamp cards, per-member progress and the append-only stamp events.
-- Every stamp runs in one tenant transaction: EnsureStampProgress,
-- LockStampProgress, InsertStampEvent rows, SetStampProgress.

-- name: CreateStampCard :one
INSERT INTO stamp_cards (
  id, tenant_id, app_id, name, description, stamps_required, reward_coupon_template_id,
  max_stamps_per_visit, created_at, updated_at
) VALUES (
  sqlc.arg('id'), sqlc.arg('tenant_id'), sqlc.arg('app_id'), sqlc.arg('name'), sqlc.narg('description'),
  sqlc.arg('stamps_required'), sqlc.arg('reward_coupon_template_id'), sqlc.arg('max_stamps_per_visit'),
  sqlc.arg('now'), sqlc.arg('now')
)
RETURNING *;

-- name: GetStampCard :one
SELECT * FROM stamp_cards WHERE app_id = $1 AND id = $2;

-- name: GetStampCardScope :one
-- System-mode lookup for routes that name only the card.
SELECT tenant_id, app_id FROM stamp_cards WHERE id = $1;

-- name: ListStampCards :many
-- Dashboard list, newest first; status NULL lists every card.
SELECT * FROM stamp_cards
WHERE app_id = sqlc.arg('app_id')
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: UpdateStampCard :one
-- Partial update. Lowering stamps_required does not complete cards on its
-- own; the next stamp does.
UPDATE stamp_cards SET
  name                      = coalesce(sqlc.narg('name'), name),
  description               = CASE WHEN sqlc.arg('set_description')::boolean THEN sqlc.narg('description') ELSE description END,
  stamps_required           = coalesce(sqlc.narg('stamps_required'), stamps_required),
  reward_coupon_template_id = coalesce(sqlc.narg('reward_coupon_template_id'), reward_coupon_template_id),
  max_stamps_per_visit      = coalesce(sqlc.narg('max_stamps_per_visit'), max_stamps_per_visit),
  status                    = coalesce(sqlc.narg('status'), status),
  updated_at                = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND id = sqlc.arg('id')
RETURNING *;

-- name: ArchiveStampCard :one
UPDATE stamp_cards SET status = 'archived', updated_at = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND id = sqlc.arg('id')
RETURNING *;

-- name: EnsureStampProgress :exec
INSERT INTO stamp_progress (member_id, stamp_card_id, tenant_id, app_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (member_id, stamp_card_id) DO NOTHING;

-- name: LockStampProgress :one
-- Serializes every stamp, adjustment and completion of one member on one card.
SELECT * FROM stamp_progress
WHERE app_id = $1 AND member_id = $2 AND stamp_card_id = $3
FOR UPDATE;

-- name: SetStampProgress :exec
UPDATE stamp_progress SET
  count           = sqlc.arg('count'),
  completions     = sqlc.arg('completions'),
  last_stamped_at = coalesce(sqlc.narg('last_stamped_at'), last_stamped_at),
  updated_at      = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND member_id = sqlc.arg('member_id') AND stamp_card_id = sqlc.arg('stamp_card_id');

-- name: GetStampProgress :one
SELECT * FROM stamp_progress WHERE app_id = $1 AND member_id = $2 AND stamp_card_id = $3;

-- name: ListMemberStampProgress :many
-- Every active card of the app with the member's progress (zero when the
-- member has none yet). include_archived adds archived cards the member has
-- progress on, for history and the PDPA export.
SELECT
  c.id AS stamp_card_id, c.name, c.description, c.stamps_required, c.max_stamps_per_visit,
  c.reward_coupon_template_id, c.status,
  coalesce(p.count, 0)::integer AS count,
  coalesce(p.completions, 0)::integer AS completions,
  p.last_stamped_at
FROM stamp_cards c
LEFT JOIN stamp_progress p
  ON p.app_id = c.app_id AND p.stamp_card_id = c.id AND p.member_id = sqlc.arg('member_id')
WHERE c.app_id = sqlc.arg('app_id')
  AND (c.status = 'active' OR (sqlc.arg('include_archived')::boolean AND p.member_id IS NOT NULL))
ORDER BY c.created_at, c.id;

-- name: InsertStampEvent :one
INSERT INTO stamp_events (
  tenant_id, app_id, member_id, stamp_card_id, txn_id, delta, count_after, completions_after,
  reason, coupon_instance_id, note, staff_id, store_id, actor_type, actor_id
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
)
RETURNING *;

-- name: ListStampEvents :many
-- A member's stamp history, newest first; stamp_card_id NULL lists every
-- card. Keyset on the last id.
SELECT * FROM stamp_events
WHERE app_id = sqlc.arg('app_id') AND member_id = sqlc.arg('member_id')
  AND (sqlc.narg('stamp_card_id')::uuid IS NULL OR stamp_card_id = sqlc.narg('stamp_card_id')::uuid)
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_limit');
