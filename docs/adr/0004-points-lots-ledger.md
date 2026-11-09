# ADR 0004: Points as lots plus an append-only ledger with stored idempotent responses

**Status:** Accepted, 2026-11-09

## Context

The v1 plan had a flat `points_ledger` with `expires_at` per row and said "expire points FIFO".
A flat ledger cannot tell which earned points were already spent, so FIFO expiry is not
computable from it. The plan also keyed idempotency on `(tenant_id, idempotency_key)` while
members and API keys are scoped to apps, and it did not say what a retry returns.

Brands lose money on double credits and double redemptions. The ledger is the product.

## Decision

- `points_lots`: one row per earn event with `earned`, `remaining` and `expires_at`.
  Redemptions and expiries consume lots in FIFO order (`expires_at NULLS LAST, created_at`).
- `points_ledger`: append-only. Every lot mutation writes a row with `delta`, `reason`,
  `lot_id`, `ref_type`/`ref_id` and `idempotency_key`. Corrections are new rows.
- `points_accounts` caches the balance and carries a `version`. Every change runs in one
  transaction: `SELECT ... FOR UPDATE` on the account, consume or create lots, insert ledger
  rows, update the cache.
- `idempotency_keys (app_id, key)` stores the SHA-256 of method + path + body and the response
  status and body. Same key and same hash returns the stored response; same key and a
  different hash returns 422. Keys are kept for 24 hours.
- Nightly job expires lots and writes `expire` rows. A daily reconciliation job checks
  `SUM(delta)` against the cached balance and `SUM(remaining)` and alerts on mismatch.
- All three tables carry `tenant_id` and `app_id` for RLS.

## Consequences

- FIFO expiry, "points expiring soon" and per-source reporting become simple queries.
- One redemption can touch several lots and therefore write several ledger rows; the API
  returns one summary.
- The row lock serializes writes per member. That is the intended behaviour and is cheap at
  the expected volume.
- Retries are safe across network failures, which the SDK and POS integrations rely on.
