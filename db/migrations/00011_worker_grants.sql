-- +goose Up
-- Phase 4 security re-review (docs/security/phase4-review.md, R3, R4, R7).
--
-- Convention: tenant tables carry tenant_id and app_id under forced RLS with
-- the standard tenant policy. This migration adds no table.

-- ---------------------------------------------------------------- worker role
-- R3: 00009 made miniloyal_worker a member of miniloyal_app, so the worker
-- inherited every grant the API has: members, channel secrets, sessions,
-- audit rows. It now holds explicit grants for what its jobs run and nothing
-- else (internal/jobs: maintenance, points expiry, the daily reconciliation).
-- Row-level security still applies: the jobs work in system mode.
--
-- Role membership is cluster-wide, so the revoke affects every database on
-- the cluster. Each database needs this migration before its worker runs
-- again. "migrate roles" no longer makes the worker a member.
REVOKE miniloyal_app FROM miniloyal_worker;

-- Points expiry: list due lots, lock the account and the lot, write the
-- expire ledger row, empty the lot, set the balance, emit points.changed.
-- FOR UPDATE needs UPDATE on at least one column, which these give.
GRANT SELECT ON points_lots TO miniloyal_worker;
GRANT UPDATE (remaining) ON points_lots TO miniloyal_worker;
GRANT SELECT ON points_accounts TO miniloyal_worker;
GRANT UPDATE (balance, lifetime_earned, updated_at) ON points_accounts TO miniloyal_worker;
-- Append-only: INSERT returns the row, and reconciliation sums it.
GRANT SELECT, INSERT ON points_ledger TO miniloyal_worker;
-- The outbox: insert only. events.Emit does not read the row back.
GRANT INSERT ON outbound_events TO miniloyal_worker;

-- Daily reconciliation: read the counters and their history, record runs.
GRANT SELECT ON stamp_progress, stamp_events, coupon_templates, coupon_instances TO miniloyal_worker;
GRANT SELECT, INSERT, UPDATE ON points_reconciliation_runs, loyalty_reconciliation_runs TO miniloyal_worker;

-- Maintenance: delete spent rows. A DELETE needs SELECT on the columns its
-- WHERE reads, so only those columns are readable.
GRANT SELECT (expires_at), DELETE ON idempotency_keys TO miniloyal_worker;
GRANT SELECT (expires_at, revoked_at), DELETE ON refresh_tokens TO miniloyal_worker;
GRANT SELECT (expires_at), DELETE ON mfa_challenges TO miniloyal_worker;
GRANT SELECT (received_at), DELETE ON webhook_inbound_events TO miniloyal_worker;
GRANT SELECT (used_at), DELETE ON scan_token_uses TO miniloyal_worker;
GRANT SELECT (used_at), DELETE ON scan_coupon_redeems TO miniloyal_worker;

-- -------------------------------------------------------- stamp note erasure
-- R4: the 00010 trigger let any update clear an adjustment's note, so the
-- application role could erase a note while the member still exists. Now
-- only an erasure may: the member must be anonymized (erasure anonymizes the
-- member first, in the same transaction), and nothing but the note may
-- change. A WHEN clause cannot read members, so the function decides.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION miniloyal_stamp_events_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
  IF NEW.note IS NULL AND OLD.reason = 'adjust'
     AND to_jsonb(NEW) - 'note' = to_jsonb(OLD) - 'note'
     AND EXISTS (SELECT 1 FROM public.members m
                 WHERE m.id = OLD.member_id AND m.app_id = OLD.app_id AND m.anonymized_at IS NOT NULL) THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'stamp_events is append-only: only the erasure of an anonymized member may clear an adjustment note'
    USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION miniloyal_stamp_events_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS stamp_events_guard ON stamp_events;
CREATE TRIGGER stamp_events_guard BEFORE UPDATE ON stamp_events
  FOR EACH ROW
  WHEN (OLD.* IS DISTINCT FROM NEW.*)
  EXECUTE FUNCTION miniloyal_stamp_events_guard();

-- ------------------------------------------------------- scan_coupon_redeems
-- R7: keep every reference inside one app, like the other Phase 4 tables.
-- The tenant and app must belong together, and a scan's jti is unique per
-- app; the redeem query's ON CONFLICT names (app_id, jti).
ALTER TABLE scan_coupon_redeems DROP CONSTRAINT scan_coupon_redeems_pkey;
ALTER TABLE scan_coupon_redeems ADD CONSTRAINT scan_coupon_redeems_pkey PRIMARY KEY (app_id, jti);
ALTER TABLE scan_coupon_redeems DROP CONSTRAINT scan_coupon_redeems_app_id_fkey;
ALTER TABLE scan_coupon_redeems ADD CONSTRAINT scan_coupon_redeems_app_fkey
  FOREIGN KEY (tenant_id, app_id) REFERENCES apps (tenant_id, id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE scan_coupon_redeems DROP CONSTRAINT IF EXISTS scan_coupon_redeems_app_fkey;
ALTER TABLE scan_coupon_redeems ADD CONSTRAINT scan_coupon_redeems_app_id_fkey
  FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE;
ALTER TABLE scan_coupon_redeems DROP CONSTRAINT scan_coupon_redeems_pkey;
ALTER TABLE scan_coupon_redeems ADD CONSTRAINT scan_coupon_redeems_pkey PRIMARY KEY (jti);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION miniloyal_stamp_events_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
  RAISE EXCEPTION 'stamp_events is append-only: only an adjustment note may be cleared'
    USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS stamp_events_guard ON stamp_events;
CREATE TRIGGER stamp_events_guard BEFORE UPDATE ON stamp_events
  FOR EACH ROW
  WHEN (OLD.* IS DISTINCT FROM NEW.* AND (NEW.note IS NOT NULL OR OLD.reason <> 'adjust'))
  EXECUTE FUNCTION miniloyal_stamp_events_guard();

REVOKE ALL ON points_lots, points_accounts, points_ledger, outbound_events,
  stamp_progress, stamp_events, coupon_templates, coupon_instances,
  points_reconciliation_runs, loyalty_reconciliation_runs,
  idempotency_keys, refresh_tokens, mfa_challenges, webhook_inbound_events,
  scan_token_uses, scan_coupon_redeems
  FROM miniloyal_worker;
-- Back to the 00009 shape. Cluster-wide, like the revoke above.
GRANT miniloyal_app TO miniloyal_worker;
