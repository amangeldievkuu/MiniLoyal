-- Coupons: templates and the instances members hold.
-- Claims and issues run in one tenant transaction after LockCouponTemplate.
-- Every time comparison takes the service clock as a parameter, never now().

-- name: CreateCouponTemplate :one
INSERT INTO coupon_templates (
  id, tenant_id, app_id, name, description, type, value, total_limit, per_member_limit,
  valid_from, valid_to, validity_days, claimable, created_at, updated_at
) VALUES (
  sqlc.arg('id'), sqlc.arg('tenant_id'), sqlc.arg('app_id'), sqlc.arg('name'), sqlc.narg('description'),
  sqlc.arg('type'), sqlc.narg('value'), sqlc.narg('total_limit'), sqlc.narg('per_member_limit'),
  sqlc.narg('valid_from'), sqlc.narg('valid_to'), sqlc.narg('validity_days'), sqlc.arg('claimable'),
  sqlc.arg('now'), sqlc.arg('now')
)
RETURNING *;

-- name: GetCouponTemplate :one
SELECT * FROM coupon_templates WHERE app_id = $1 AND id = $2;

-- name: GetCouponTemplateScope :one
-- System-mode lookup for routes that name only the template: resolves the
-- tenant and app before the tenant transaction opens.
SELECT tenant_id, app_id FROM coupon_templates WHERE id = $1;

-- name: ListCouponTemplates :many
-- Dashboard list, newest first; status NULL lists every template.
SELECT * FROM coupon_templates
WHERE app_id = sqlc.arg('app_id')
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: ListCouponOffers :many
-- Templates a member may claim right now, with how many the member already
-- holds. The claim itself re-checks every limit under the template lock.
SELECT t.*,
  (SELECT count(*) FROM coupon_instances i
   WHERE i.app_id = t.app_id AND i.template_id = t.id AND i.member_id = sqlc.arg('member_id'))::integer AS member_claimed
FROM coupon_templates t
WHERE t.app_id = sqlc.arg('app_id')
  AND t.status = 'active'
  AND t.claimable
  AND (t.valid_from IS NULL OR t.valid_from <= sqlc.arg('now'))
  AND (t.valid_to IS NULL OR t.valid_to > sqlc.arg('now'))
  AND (t.total_limit IS NULL OR t.claimed_count < t.total_limit)
ORDER BY t.created_at DESC, t.id DESC;

-- name: UpdateCouponTemplate :one
-- Partial update. Nullable fields take a set_* flag so they can be cleared.
-- The type never changes. A total_limit below claimed_count fails
-- coupon_templates_limit_check (SQLSTATE 23514).
UPDATE coupon_templates SET
  name             = coalesce(sqlc.narg('name'), name),
  description      = CASE WHEN sqlc.arg('set_description')::boolean THEN sqlc.narg('description') ELSE description END,
  value            = CASE WHEN sqlc.arg('set_value')::boolean THEN sqlc.narg('value') ELSE value END,
  total_limit      = CASE WHEN sqlc.arg('set_total_limit')::boolean THEN sqlc.narg('total_limit') ELSE total_limit END,
  per_member_limit = CASE WHEN sqlc.arg('set_per_member_limit')::boolean THEN sqlc.narg('per_member_limit') ELSE per_member_limit END,
  valid_from       = CASE WHEN sqlc.arg('set_valid_from')::boolean THEN sqlc.narg('valid_from') ELSE valid_from END,
  valid_to         = CASE WHEN sqlc.arg('set_valid_to')::boolean THEN sqlc.narg('valid_to') ELSE valid_to END,
  validity_days    = CASE WHEN sqlc.arg('set_validity_days')::boolean THEN sqlc.narg('validity_days') ELSE validity_days END,
  claimable        = coalesce(sqlc.narg('claimable'), claimable),
  status           = coalesce(sqlc.narg('status'), status),
  updated_at       = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND id = sqlc.arg('id')
RETURNING *;

-- name: ArchiveCouponTemplate :one
-- Archived templates take no new claims or issues; held instances stay valid.
UPDATE coupon_templates SET status = 'archived', updated_at = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND id = sqlc.arg('id')
RETURNING *;

-- name: LockCouponTemplate :one
-- Serializes claims and issues of one template.
SELECT * FROM coupon_templates WHERE app_id = $1 AND id = $2 FOR UPDATE;

-- name: IncrementCouponTemplateClaimed :one
-- Counts one more issued instance. No row means total_limit is reached, a
-- second guard behind the check the service makes under the lock.
UPDATE coupon_templates SET claimed_count = claimed_count + 1
WHERE app_id = $1 AND id = $2 AND (total_limit IS NULL OR claimed_count < total_limit)
RETURNING claimed_count;

-- name: CountMemberCouponInstances :one
-- Instances of every status count toward per_member_limit: a void does not
-- hand the claim back, just as it does not lower claimed_count.
SELECT count(*)::integer FROM coupon_instances
WHERE app_id = $1 AND template_id = $2 AND member_id = $3;

-- name: InsertCouponInstance :one
-- code is unique per app; on a unique violation of coupon_instances_app_id_code_key
-- generate a new code and retry.
INSERT INTO coupon_instances (
  id, tenant_id, app_id, template_id, member_id, code, source, txn_id,
  issued_at, expires_at, issued_by_type, issued_by_id, created_at, updated_at
) VALUES (
  sqlc.arg('id'), sqlc.arg('tenant_id'), sqlc.arg('app_id'), sqlc.arg('template_id'), sqlc.arg('member_id'),
  sqlc.arg('code'), sqlc.arg('source'), sqlc.arg('txn_id'), sqlc.arg('issued_at'), sqlc.narg('expires_at'),
  sqlc.arg('issued_by_type'), sqlc.narg('issued_by_id'), sqlc.arg('issued_at'), sqlc.arg('issued_at')
)
RETURNING *;

-- name: GetCouponInstance :one
SELECT * FROM coupon_instances WHERE app_id = $1 AND id = $2;

-- name: GetCouponInstanceByCode :one
SELECT * FROM coupon_instances WHERE app_id = $1 AND code = $2;

-- name: GetCouponInstanceScope :one
-- System-mode lookup for routes that name only the instance.
SELECT tenant_id, app_id, member_id FROM coupon_instances WHERE id = $1;

-- name: ListMemberCoupons :many
-- A member's coupons with template details, newest first. effective_status is
-- 'expired' for an issued instance at or past expires_at (as of the passed
-- clock time). state filters on effective_status; NULL lists all. Keyset on
-- (issued_at, id) of the last row.
SELECT
  i.id, i.tenant_id, i.app_id, i.template_id, i.member_id, i.code, i.status, i.source, i.txn_id,
  i.issued_at, i.expires_at, i.issued_by_type, i.issued_by_id,
  i.redeemed_at, i.redeemed_by_staff_id, i.redeemed_at_store_id,
  i.voided_at, i.voided_by_type, i.voided_by_id, i.void_reason, i.created_at, i.updated_at,
  (CASE WHEN i.status = 'issued' AND i.expires_at IS NOT NULL AND i.expires_at <= sqlc.arg('now')
        THEN 'expired' ELSE i.status END)::text AS effective_status,
  t.name AS template_name, t.description AS template_description,
  t.type AS template_type, t.value AS template_value
FROM coupon_instances i
JOIN coupon_templates t ON t.app_id = i.app_id AND t.id = i.template_id
WHERE i.app_id = sqlc.arg('app_id') AND i.member_id = sqlc.arg('member_id')
  AND (sqlc.narg('state')::text IS NULL OR sqlc.narg('state')::text =
       CASE WHEN i.status = 'issued' AND i.expires_at IS NOT NULL AND i.expires_at <= sqlc.arg('now')
            THEN 'expired' ELSE i.status END)
  AND (sqlc.narg('before_issued_at')::timestamptz IS NULL
       OR (i.issued_at, i.id) < (sqlc.narg('before_issued_at')::timestamptz, sqlc.narg('before_id')::uuid))
ORDER BY i.issued_at DESC, i.id DESC
LIMIT sqlc.arg('page_limit');

-- name: RedeemCouponInstance :one
-- The race-safe redeem (plan 5.5): one statement, so of any number of
-- concurrent redeems exactly one gets a row back. No row means not found in
-- this app, not this member's, already redeemed or voided, or expired.
UPDATE coupon_instances SET
  status               = 'redeemed',
  redeemed_at          = sqlc.arg('now'),
  redeemed_by_staff_id = sqlc.narg('staff_id'),
  redeemed_at_store_id = sqlc.narg('store_id'),
  updated_at           = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id')
  AND id = sqlc.arg('id')
  AND member_id = sqlc.arg('member_id')
  AND status = 'issued'
  AND (expires_at IS NULL OR expires_at > sqlc.arg('now'))
RETURNING *;

-- name: VoidCouponInstance :one
-- Only an issued instance can be voided (expired ones included). No row means
-- not found in this app or already redeemed or voided.
UPDATE coupon_instances SET
  status         = 'voided',
  voided_at      = sqlc.arg('now'),
  voided_by_type = sqlc.arg('voided_by_type'),
  voided_by_id   = sqlc.narg('voided_by_id'),
  void_reason    = sqlc.narg('void_reason'),
  updated_at     = sqlc.arg('now')
WHERE app_id = sqlc.arg('app_id') AND id = sqlc.arg('id') AND status = 'issued'
RETURNING *;
