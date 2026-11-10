-- Stamp cards, per-member progress and the append-only stamp events.
-- Every stamp runs in one tenant transaction: EnsureStampProgress,
-- LockStampProgress, InsertStampEvent rows, SetStampProgress.

-- name: CreateStampCard :one
INSERT INTO stamp_cards (
  id, tenant_id, app_id, name, description, stamps_required, reward_coupon_template_id,
  max_stamps_per_visit, created_at, updated_at
) VALUES (
  sqlc.arg('id'), sqlc.arg('tenant_id'), sqlc.arg('app_id'), sqlc.arg('name'), sqlc.narg('description'),
  sqlc.arg('stamps_required'), sqlc.narg('reward_coupon_template_id'), sqlc.arg('max_stamps_per_visit'),
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
-- Partial update: a NULL argument keeps the column; set_* flags let
-- description and reward_coupon_template_id be cleared. stamps_required is
-- fixed once StampCardInUse is true (the service checks). A reward template
-- of another app fails the foreign key (SQLSTATE 23503). status 'active'
-- reactivates an archived card.
UPDATE stamp_cards SET
  name                      = coalesce(sqlc.narg('name'), name),
  description               = CASE WHEN sqlc.arg('set_description')::boolean THEN sqlc.narg('description') ELSE description END,
  stamps_required           = coalesce(sqlc.narg('stamps_required'), stamps_required),
  reward_coupon_template_id = CASE WHEN sqlc.arg('set_reward_coupon_template_id')::boolean
                                   THEN sqlc.narg('reward_coupon_template_id') ELSE reward_coupon_template_id END,
  max_stamps_per_visit      = coalesce(sqlc.narg('max_stamps_per_visit'), max_stamps_per_visit),
  status                    = coalesce(sqlc.narg('status'), status),
  updated_at                = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND id = sqlc.arg('id')
RETURNING *;

-- name: ArchiveStampCard :one
UPDATE stamp_cards SET status = 'archived', updated_at = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND id = sqlc.arg('id')
RETURNING *;

-- name: StampCardInUse :one
-- True once any member has a stamp event on the card.
SELECT EXISTS (SELECT 1 FROM stamp_events WHERE app_id = $1 AND stamp_card_id = $2)::boolean AS in_use;

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
-- member has none yet) and the reward template summary (NULL columns when
-- the card has no reward). include_archived adds archived cards the member
-- has progress on, for history and the PDPA export. progress_updated_at is
-- NULL until the member's first stamp or adjustment. unrewarded_completions
-- counts completion events whose reward could not be issued (template sold
-- out or archived): the stamp service writes them with no coupon and the
-- note 'reward_unavailable'. The note, not the card's current reward, marks
-- them because a reward set later must not count earlier rewardless
-- completions.
SELECT
  c.id AS stamp_card_id, c.name, c.description, c.stamps_required, c.max_stamps_per_visit,
  c.reward_coupon_template_id, c.status,
  coalesce(p.count, 0)::integer AS count,
  coalesce(p.completions, 0)::integer AS completions,
  (SELECT count(*) FROM stamp_events e
    WHERE e.app_id = c.app_id AND e.stamp_card_id = c.id AND e.member_id = sqlc.arg('member_id')
      AND e.reason = 'completion' AND e.coupon_instance_id IS NULL
      AND e.note = 'reward_unavailable')::integer AS unrewarded_completions,
  p.last_stamped_at,
  p.updated_at AS progress_updated_at,
  r.name AS reward_name, r.description AS reward_description,
  r.type AS reward_type, r.value AS reward_value
FROM stamp_cards c
LEFT JOIN stamp_progress p
  ON p.app_id = c.app_id AND p.stamp_card_id = c.id AND p.member_id = sqlc.arg('member_id')
LEFT JOIN coupon_templates r
  ON r.app_id = c.app_id AND r.id = c.reward_coupon_template_id
WHERE c.app_id = sqlc.arg('app_id')
  AND (c.status = 'active' OR (sqlc.arg('include_archived')::boolean AND p.member_id IS NOT NULL))
ORDER BY c.created_at, c.id;

-- name: InsertStampEvent :one
INSERT INTO stamp_events (
  tenant_id, app_id, member_id, stamp_card_id, txn_id, delta, count_after, completions_after,
  reason, coupon_instance_id, scan_id, note, staff_id, store_id, actor_type, actor_id
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
)
RETURNING *;

-- name: SumScanStamps :one
-- Stamps already given on one scan to one card, for max_stamps_per_visit.
-- Call it after LockStampProgress so concurrent requests on the same scan
-- see each other's rows.
SELECT coalesce(sum(delta), 0)::integer AS stamps
FROM stamp_events
WHERE app_id = $1 AND stamp_card_id = $2 AND scan_id = $3 AND reason = 'stamp';

-- name: ListStampEvents :many
-- A member's stamp history, newest first; stamp_card_id NULL lists every
-- card. Keyset on the last id.
SELECT * FROM stamp_events
WHERE app_id = sqlc.arg('app_id') AND member_id = sqlc.arg('member_id')
  AND (sqlc.narg('stamp_card_id')::uuid IS NULL OR stamp_card_id = sqlc.narg('stamp_card_id')::uuid)
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_limit');
