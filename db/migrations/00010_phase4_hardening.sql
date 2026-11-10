-- +goose Up
-- Phase 4 security review follow-up (docs/security/phase4-review.md).
--
-- Convention: tenant tables carry tenant_id and app_id under the standard
-- forced tenant policy; system tables (no tenant column) get a forced
-- system_only policy, like points_reconciliation_runs in 00007.
--
-- The worker role needs no grant here: miniloyal_worker is a member of
-- miniloyal_app and inherits its SELECT and INSERT on outbound_events, the
-- ledgers and the new tables below, so points expiry can emit
-- points.changed and the reconciliation can record its runs.

-- ------------------------------------------------------------------ visit window
-- max_stamps_per_visit counts every stamp the member got on the card in the
-- last 30 minutes (internal/stamp VisitWindow), whatever the scan, so a new
-- scan no longer opens a new visit. stamp_events.created_at now comes from
-- the service clock. The sum runs under the stamp_progress row lock.
CREATE INDEX stamp_events_visit_idx ON stamp_events (app_id, stamp_card_id, member_id, created_at)
  WHERE reason = 'stamp';
DROP INDEX IF EXISTS stamp_events_scan_idx;

-- ------------------------------------------------------------- coupon redeems
-- One scan token pays for at most three coupon redemptions
-- (internal/coupon MaxRedeemsPerScan). One row per scan that redeemed; the
-- counter goes up in an INSERT ... ON CONFLICT DO UPDATE inside the redeem
-- transaction, so concurrent redeems on one scan serialize on the row and a
-- refused redeem rolls its count back. Separate from scan_token_uses, where
-- a row means the scan already paid for a points redemption.
CREATE TABLE scan_coupon_redeems (
  jti        text PRIMARY KEY CHECK (char_length(jti) BETWEEN 1 AND 128),
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id     uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  redeems    integer NOT NULL CHECK (redeems >= 1),
  used_at    timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scan_coupon_redeems_app_idx ON scan_coupon_redeems (app_id, used_at);
CREATE INDEX scan_coupon_redeems_used_idx ON scan_coupon_redeems (used_at);
ALTER TABLE scan_coupon_redeems ENABLE ROW LEVEL SECURITY;
ALTER TABLE scan_coupon_redeems FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON scan_coupon_redeems
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
-- DELETE for the maintenance job, which drops rows older than a day.
GRANT SELECT, INSERT, DELETE ON scan_coupon_redeems TO miniloyal_app;
GRANT UPDATE (redeems, used_at) ON scan_coupon_redeems TO miniloyal_app;

-- ------------------------------------------------------------- erasure of notes
-- A PDPA erasure blanks the free text staff typed about the member: the
-- notes of stamp adjustments and the reasons of voided coupons.
-- coupon_instances.void_reason is already updatable (00008). stamp_events
-- stays append-only: the application may update the note column alone, and
-- the trigger lets that update only clear an adjustment's note. Completion
-- rows keep their reward_unavailable marker.
GRANT UPDATE (note) ON stamp_events TO miniloyal_app;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION miniloyal_stamp_events_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
  RAISE EXCEPTION 'stamp_events is append-only: only an adjustment note may be cleared'
    USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION miniloyal_stamp_events_guard() FROM PUBLIC;
CREATE TRIGGER stamp_events_guard BEFORE UPDATE ON stamp_events
  FOR EACH ROW
  WHEN (OLD.* IS DISTINCT FROM NEW.* AND (NEW.note IS NOT NULL OR OLD.reason <> 'adjust'))
  EXECUTE FUNCTION miniloyal_stamp_events_guard();

-- ----------------------------------------------------------- reconciliation
-- The daily reconciliation also checks stamp progress against stamp_events
-- and each coupon template's claimed_count against its instances. Details
-- name members, cards and templates of every tenant, so only system mode
-- may read or write the runs.
CREATE TABLE loyalty_reconciliation_runs (
  id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  started_at         timestamptz NOT NULL,
  finished_at        timestamptz,
  progress_checked   integer NOT NULL DEFAULT 0,
  templates_checked  integer NOT NULL DEFAULT 0,
  mismatches         integer NOT NULL DEFAULT 0,
  details            jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at         timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE loyalty_reconciliation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE loyalty_reconciliation_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY system_only ON loyalty_reconciliation_runs
  USING (app_is_system())
  WITH CHECK (app_is_system());
GRANT SELECT, INSERT, UPDATE ON loyalty_reconciliation_runs TO miniloyal_app;

-- ------------------------------------------------------------------ 00008 fix
-- The comment on coupon_templates_limit_check in 00008 is wrong: lowering
-- total_limit below claimed_count is refused by the service with 422
-- validation_failed (field total_limit), not 409 coupon_template_in_use.
COMMENT ON CONSTRAINT coupon_templates_limit_check ON coupon_templates IS
  'total_limit never goes below claimed_count; the API refuses such an update with 422 validation_failed on total_limit.';

-- --------------------------------------------------------------------- River
-- cmd/api does not enqueue jobs yet, so miniloyal_api loses the insert
-- grants 00009 gave it on river_job. miniloyal_secure_river() is redefined
-- without them, so "migrate up" does not hand them back after a River
-- migration. When the API starts enqueueing, a migration grants SELECT and
-- INSERT on river_job, UPDATE (kind) and the sequence again. The guard
-- trigger stays: no role but the worker may change a job row.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION miniloyal_secure_river() RETURNS void
LANGUAGE plpgsql SET search_path = public, pg_catalog AS $$
DECLARE r record;
BEGIN
  FOR r IN SELECT c.oid::regclass AS rel FROM pg_class c
           WHERE c.relnamespace = 'public'::regnamespace AND c.relname LIKE 'river\_%'
             AND c.relkind IN ('r', 'p')
  LOOP
    EXECUTE format('ALTER TABLE %s OWNER TO miniloyal_worker', r.rel);
    EXECUTE format('REVOKE ALL ON %s FROM PUBLIC, miniloyal_app, miniloyal_api', r.rel);
  END LOOP;
  FOR r IN SELECT c.oid::regclass AS rel,
                  EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
                          AND d.objid = c.oid AND d.deptype IN ('a', 'i')) AS linked
           FROM pg_class c
           WHERE c.relnamespace = 'public'::regnamespace AND c.relname LIKE 'river\_%' AND c.relkind = 'S'
  LOOP
    IF NOT r.linked THEN
      EXECUTE format('ALTER SEQUENCE %s OWNER TO miniloyal_worker', r.rel);
    END IF;
    EXECUTE format('REVOKE ALL ON SEQUENCE %s FROM PUBLIC, miniloyal_app, miniloyal_api', r.rel);
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p
           WHERE p.pronamespace = 'public'::regnamespace AND p.proname LIKE 'river\_%'
  LOOP
    EXECUTE format('ALTER FUNCTION %s OWNER TO miniloyal_worker', r.sig);
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM miniloyal_app, miniloyal_api', r.sig);
  END LOOP;
  FOR r IN SELECT t.oid::regtype AS typ FROM pg_type t
           WHERE t.typnamespace = 'public'::regnamespace AND t.typname LIKE 'river\_%'
             AND t.typrelid = 0
  LOOP
    EXECUTE format('ALTER TYPE %s OWNER TO miniloyal_worker', r.typ);
  END LOOP;

  IF to_regclass('public.river_job') IS NOT NULL THEN
    DROP TRIGGER IF EXISTS miniloyal_river_job_guard ON river_job;
    CREATE TRIGGER miniloyal_river_job_guard BEFORE UPDATE ON river_job
      FOR EACH ROW
      WHEN (NOT pg_has_role(current_user, 'miniloyal_worker', 'USAGE') AND OLD.* IS DISTINCT FROM NEW.*)
      EXECUTE FUNCTION miniloyal_river_job_guard();
  END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION miniloyal_secure_river() FROM PUBLIC;

SELECT miniloyal_secure_river();

-- +goose Down
-- Back to the 00009 function, which gives miniloyal_api its River insert
-- grants, and apply it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION miniloyal_secure_river() RETURNS void
LANGUAGE plpgsql SET search_path = public, pg_catalog AS $$
DECLARE r record;
BEGIN
  FOR r IN SELECT c.oid::regclass AS rel FROM pg_class c
           WHERE c.relnamespace = 'public'::regnamespace AND c.relname LIKE 'river\_%'
             AND c.relkind IN ('r', 'p')
  LOOP
    EXECUTE format('ALTER TABLE %s OWNER TO miniloyal_worker', r.rel);
    EXECUTE format('REVOKE ALL ON %s FROM PUBLIC, miniloyal_app, miniloyal_api', r.rel);
  END LOOP;
  FOR r IN SELECT c.oid::regclass AS rel,
                  EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
                          AND d.objid = c.oid AND d.deptype IN ('a', 'i')) AS linked
           FROM pg_class c
           WHERE c.relnamespace = 'public'::regnamespace AND c.relname LIKE 'river\_%' AND c.relkind = 'S'
  LOOP
    IF NOT r.linked THEN
      EXECUTE format('ALTER SEQUENCE %s OWNER TO miniloyal_worker', r.rel);
    END IF;
    EXECUTE format('REVOKE ALL ON SEQUENCE %s FROM PUBLIC, miniloyal_app, miniloyal_api', r.rel);
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p
           WHERE p.pronamespace = 'public'::regnamespace AND p.proname LIKE 'river\_%'
  LOOP
    EXECUTE format('ALTER FUNCTION %s OWNER TO miniloyal_worker', r.sig);
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM miniloyal_app, miniloyal_api', r.sig);
  END LOOP;
  FOR r IN SELECT t.oid::regtype AS typ FROM pg_type t
           WHERE t.typnamespace = 'public'::regnamespace AND t.typname LIKE 'river\_%'
             AND t.typrelid = 0
  LOOP
    EXECUTE format('ALTER TYPE %s OWNER TO miniloyal_worker', r.typ);
  END LOOP;

  IF to_regclass('public.river_job') IS NOT NULL THEN
    GRANT SELECT, INSERT ON river_job TO miniloyal_api;
    GRANT UPDATE (kind) ON river_job TO miniloyal_api;
    GRANT USAGE ON SEQUENCE river_job_id_seq TO miniloyal_api;
    DROP TRIGGER IF EXISTS miniloyal_river_job_guard ON river_job;
    CREATE TRIGGER miniloyal_river_job_guard BEFORE UPDATE ON river_job
      FOR EACH ROW
      WHEN (NOT pg_has_role(current_user, 'miniloyal_worker', 'USAGE') AND OLD.* IS DISTINCT FROM NEW.*)
      EXECUTE FUNCTION miniloyal_river_job_guard();
  END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION miniloyal_secure_river() FROM PUBLIC;
SELECT miniloyal_secure_river();

COMMENT ON CONSTRAINT coupon_templates_limit_check ON coupon_templates IS NULL;
DROP TABLE IF EXISTS loyalty_reconciliation_runs;
DROP TRIGGER IF EXISTS stamp_events_guard ON stamp_events;
DROP FUNCTION IF EXISTS miniloyal_stamp_events_guard();
REVOKE UPDATE (note) ON stamp_events FROM miniloyal_app;
DROP TABLE IF EXISTS scan_coupon_redeems;
CREATE INDEX IF NOT EXISTS stamp_events_scan_idx ON stamp_events (app_id, stamp_card_id, scan_id) WHERE scan_id IS NOT NULL;
DROP INDEX IF EXISTS stamp_events_visit_idx;
