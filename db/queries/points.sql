-- Points: rules, accounts, FIFO lots and the append-only ledger.
-- Every balance change runs in one tenant transaction after LockPointsAccount.

-- name: GetPointsRules :one
SELECT * FROM points_rules WHERE app_id = $1;

-- name: UpsertPointsRules :one
INSERT INTO points_rules (app_id, tenant_id, currency, spend_per_point, points_per_unit, min_spend, max_points_per_txn, expiry_days, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (app_id) DO UPDATE SET
  currency           = EXCLUDED.currency,
  spend_per_point    = EXCLUDED.spend_per_point,
  points_per_unit    = EXCLUDED.points_per_unit,
  min_spend          = EXCLUDED.min_spend,
  max_points_per_txn = EXCLUDED.max_points_per_txn,
  expiry_days        = EXCLUDED.expiry_days,
  enabled            = EXCLUDED.enabled,
  updated_at         = now()
RETURNING *;

-- name: EnsurePointsAccount :exec
INSERT INTO points_accounts (member_id, tenant_id, app_id)
VALUES ($1, $2, $3)
ON CONFLICT (member_id) DO NOTHING;

-- name: LockPointsAccount :one
SELECT * FROM points_accounts WHERE member_id = $1 FOR UPDATE;

-- name: GetPointsAccount :one
SELECT * FROM points_accounts WHERE member_id = $1;

-- name: SetPointsBalance :exec
UPDATE points_accounts SET
  balance         = $2,
  lifetime_earned = lifetime_earned + sqlc.arg('earned_delta')::bigint,
  updated_at      = now()
WHERE member_id = $1;

-- name: InsertPointsLot :one
INSERT INTO points_lots (tenant_id, app_id, member_id, earned, remaining, expires_at)
VALUES ($1, $2, $3, $4, $4, $5)
RETURNING *;

-- name: ListOpenLotsForUpdate :many
-- FIFO: soonest expiry first, never-expiring last, then oldest.
SELECT * FROM points_lots
WHERE member_id = $1 AND remaining > 0
ORDER BY expires_at NULLS LAST, created_at, id
FOR UPDATE;

-- name: SetLotRemaining :exec
UPDATE points_lots SET remaining = $2 WHERE id = $1;

-- name: InsertLedgerEntry :one
INSERT INTO points_ledger (
  tenant_id, app_id, member_id, txn_id, lot_id, delta, balance_after, reason,
  spend_amount, ref_type, ref_id, note, store_id, actor_type, actor_id
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
)
RETURNING *;

-- name: ListExpiringSoon :many
-- The next lots to expire, for the member's summary.
SELECT expires_at, sum(remaining)::bigint AS points
FROM points_lots
WHERE member_id = $1 AND remaining > 0 AND expires_at IS NOT NULL AND expires_at > sqlc.arg('now')
GROUP BY expires_at
ORDER BY expires_at
LIMIT 3;

-- name: ListPointsTxnPage :many
-- One row per operation (txn_id), newest first; keyset on the last ledger id.
SELECT txn_id, max(id)::bigint AS last_id
FROM points_ledger
WHERE member_id = sqlc.arg('member_id')
GROUP BY txn_id
HAVING sqlc.narg('before_id')::bigint IS NULL OR max(id) < sqlc.narg('before_id')::bigint
ORDER BY max(id) DESC
LIMIT sqlc.arg('page_limit');

-- name: ListLedgerByTxns :many
SELECT * FROM points_ledger
WHERE member_id = sqlc.arg('member_id') AND txn_id = ANY(sqlc.arg('txn_ids')::uuid[])
ORDER BY id;

-- name: LockPointsLot :one
SELECT * FROM points_lots WHERE id = $1 FOR UPDATE;

-- name: ListDueLots :many
-- Expiry job: lots past their expiry that still hold points, oldest first.
-- Lots that failed earlier in the same run are skipped so one bad account
-- cannot stall expiry for everyone.
SELECT * FROM points_lots
WHERE remaining > 0 AND expires_at IS NOT NULL AND expires_at <= sqlc.arg('now')
  AND NOT (id = ANY(sqlc.arg('excluded')::bigint[]))
ORDER BY expires_at, id
LIMIT sqlc.arg('batch');

-- name: ListMemberDueLotsForUpdate :many
-- A write transaction expires the locked member's due lots before it touches
-- the balance, so expired points are never spent.
SELECT * FROM points_lots
WHERE member_id = $1 AND remaining > 0 AND expires_at IS NOT NULL AND expires_at <= sqlc.arg('now')
ORDER BY expires_at, id
FOR UPDATE;

-- name: UseScanToken :execrows
-- Zero rows means the scan token already paid for a redemption.
INSERT INTO scan_token_uses (jti, tenant_id, app_id, used_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (jti) DO NOTHING;

-- name: DeleteOldScanTokenUses :execrows
-- Scan tokens live five minutes; a day of history is plenty.
DELETE FROM scan_token_uses WHERE used_at < sqlc.arg('before');

-- name: ReconcilePointsAccounts :many
-- Accounts whose cached balance disagrees with the ledger or the open lots.
SELECT
  a.member_id,
  a.tenant_id,
  a.app_id,
  a.balance,
  coalesce((SELECT sum(l.delta) FROM points_ledger l WHERE l.member_id = a.member_id), 0)::bigint AS ledger_sum,
  coalesce((SELECT sum(t.remaining) FROM points_lots t WHERE t.member_id = a.member_id), 0)::bigint AS lots_remaining
FROM points_accounts a
WHERE a.balance <> coalesce((SELECT sum(l.delta) FROM points_ledger l WHERE l.member_id = a.member_id), 0)
   OR a.balance <> coalesce((SELECT sum(t.remaining) FROM points_lots t WHERE t.member_id = a.member_id), 0);

-- name: CountPointsAccounts :one
SELECT count(*) FROM points_accounts;

-- name: StartReconciliationRun :one
INSERT INTO points_reconciliation_runs (started_at) VALUES ($1) RETURNING id;

-- name: FinishReconciliationRun :exec
UPDATE points_reconciliation_runs
SET finished_at = $2, accounts_checked = $3, mismatches = $4, details = $5
WHERE id = $1;

-- name: GetMemberQRVersion :one
SELECT qr_key_version FROM members WHERE id = $1;

-- name: RotateMemberQRKey :one
UPDATE members SET qr_key_version = qr_key_version + 1, updated_at = now()
WHERE id = $1 AND anonymized_at IS NULL
RETURNING qr_key_version;
