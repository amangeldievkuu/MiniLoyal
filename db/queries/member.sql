-- name: UpsertMemberFromLogin :one
-- Creates the member on first login or refreshes the LINE profile fields.
INSERT INTO members (id, tenant_id, app_id, line_user_id, display_name, picture_url, language, last_login_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (app_id, line_user_id) DO UPDATE SET
  display_name  = coalesce(EXCLUDED.display_name, members.display_name),
  picture_url   = coalesce(EXCLUDED.picture_url, members.picture_url),
  language      = coalesce(EXCLUDED.language, members.language),
  last_login_at = now(),
  updated_at    = now()
RETURNING *, (xmax = 0) AS created;

-- name: UpsertMemberFromFollow :one
-- A follow webhook may arrive before the member ever opened the MINI App.
INSERT INTO members (id, tenant_id, app_id, line_user_id, is_friend, friend_changed_at)
VALUES ($1, $2, $3, $4, true, now())
ON CONFLICT (app_id, line_user_id) DO UPDATE SET
  is_friend         = true,
  friend_changed_at = now(),
  updated_at        = now()
RETURNING *;

-- name: SetMemberFriendByLineUserID :execrows
UPDATE members SET is_friend = $3, friend_changed_at = now(), updated_at = now()
WHERE app_id = $1 AND line_user_id = $2 AND is_friend <> $3;

-- name: GetMember :one
SELECT * FROM members WHERE id = $1;

-- name: GetMemberTenant :one
SELECT tenant_id FROM members WHERE id = $1;

-- name: GetMemberByLineUserID :one
SELECT * FROM members WHERE app_id = $1 AND line_user_id = $2;

-- name: UpdateMemberProfile :one
UPDATE members SET
  display_name  = coalesce(sqlc.narg('display_name'), display_name),
  phone         = CASE WHEN sqlc.arg('set_phone')::boolean THEN sqlc.narg('phone') ELSE phone END,
  birthday      = CASE WHEN sqlc.arg('set_birthday')::boolean THEN sqlc.narg('birthday') ELSE birthday END,
  gender        = CASE WHEN sqlc.arg('set_gender')::boolean THEN sqlc.narg('gender') ELSE gender END,
  custom_fields = coalesce(sqlc.narg('custom_fields'), custom_fields),
  updated_at    = now()
WHERE id = sqlc.arg('id') AND anonymized_at IS NULL
RETURNING *;

-- name: SetMemberConsent :one
UPDATE members SET consent_version = $2, consent_at = now(), updated_at = now()
WHERE id = $1 AND anonymized_at IS NULL
RETURNING *;

-- name: InsertMemberConsent :exec
INSERT INTO member_consents (tenant_id, app_id, member_id, version, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListMemberConsents :many
SELECT * FROM member_consents WHERE member_id = $1 ORDER BY id DESC;

-- name: AnonymizeMember :execrows
-- PDPA erasure: personal data is blanked in place so ledgers that reference
-- the member stay consistent. The LINE user ID is replaced so the same LINE
-- user can register again as a new member.
UPDATE members SET
  line_user_id   = 'erased:' || id::text,
  display_name   = NULL,
  picture_url    = NULL,
  language       = NULL,
  phone          = NULL,
  birthday       = NULL,
  gender         = NULL,
  custom_fields  = '{}'::jsonb,
  consent_version = NULL,
  consent_at     = NULL,
  is_friend      = false,
  anonymized_at  = now(),
  updated_at     = now()
WHERE id = $1 AND anonymized_at IS NULL;

-- name: ListMembers :many
-- Newest first, keyset paging on (created_at, id). q matches the display name
-- (case-insensitive prefix or substring) or the exact LINE user ID.
SELECT * FROM members
WHERE app_id = sqlc.arg('app_id')
  AND anonymized_at IS NULL
  AND (
    sqlc.narg('q')::text IS NULL
    OR lower(display_name) LIKE '%' || lower(sqlc.narg('q')::text) || '%'
    OR line_user_id = sqlc.narg('q')::text
  )
  AND (
    sqlc.narg('before_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('before_created_at')::timestamptz, sqlc.narg('before_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');

-- name: CountMembers :one
SELECT count(*) FROM members WHERE app_id = $1 AND anonymized_at IS NULL;
