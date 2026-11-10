-- +goose Up
-- Closes the Phase 3 finding that River's job tables were reached through
-- miniloyal_app, so SQL injection in the API could rewrite or delete jobs.
--
-- Convention (see .claude/rules/sql.md): every tenant table carries tenant_id
-- (and app_id for member-scoped data) under forced RLS. This migration adds no
-- tenant table. River's tables hold job IDs and kinds, never tenant data, so
-- they stay without RLS and are protected by ownership and grants instead.
--
-- Roles after this migration:
--   * miniloyal_worker is the login role cmd/worker connects as. It owns
--     River's tables, types and functions (River's reindexer needs ownership)
--     and is a member of miniloyal_app for the tenant tables its jobs touch.
--     It is NOBYPASSRLS on purpose: the expiry, reconciliation and maintenance
--     jobs already run their cross-tenant work in system mode (db.WithSystem,
--     app_is_system()), so row-level security still applies to the worker.
--   * miniloyal_api keeps only what River's insert-only client needs:
--     SELECT and INSERT on river_job (the insert returns the row), UPDATE of
--     the kind column alone (River's unique insert ends in
--     ON CONFLICT DO UPDATE SET kind = river_job.kind) and the id sequence.
--     A trigger rejects any update by a non-worker role that changes a row,
--     so that column grant cannot rewrite a job. It has nothing on
--     river_leader, river_queue, river_migration or River's client tables.
--   * miniloyal_app has nothing on River's objects any more.
--
-- River's tables are created by rivermigrate after the goose migrations run,
-- so on a fresh database they do not exist yet. migrate.Up therefore calls
-- miniloyal_secure_river() again after every River migration; the call below
-- covers databases where River's tables already exist.
--
-- "migrate roles" creates miniloyal_worker with LOGIN and its password before
-- this runs. The block below only covers a migration applied without it.

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'miniloyal_worker') THEN
    CREATE ROLE miniloyal_worker NOLOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_auth_members m
                 JOIN pg_roles g ON g.oid = m.roleid JOIN pg_roles u ON u.oid = m.member
                 WHERE g.rolname = 'miniloyal_app' AND u.rolname = 'miniloyal_worker') THEN
    GRANT miniloyal_app TO miniloyal_worker;
  END IF;
END
$$;
-- +goose StatementEnd

-- miniloyal_river_job_guard stops a role that is not the worker from changing
-- a job row. River's insert conflict path updates kind to its own value, which
-- changes nothing and passes; any real change raises.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION miniloyal_river_job_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
  RAISE EXCEPTION 'only miniloyal_worker may change river_job rows'
    USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION miniloyal_river_job_guard() FROM PUBLIC;

-- miniloyal_secure_river hands every River object in the public schema to
-- miniloyal_worker, strips earlier grants and gives miniloyal_api its insert
-- grants. It is idempotent and runs as the schema owner.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION miniloyal_secure_river() RETURNS void
LANGUAGE plpgsql SET search_path = public, pg_catalog AS $$
DECLARE r record;
BEGIN
  -- Tables first: their indexes and serial sequences follow the owner.
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
  -- Functions keep PUBLIC's default EXECUTE: River's insert query calls
  -- river_job_state_in_bitmask, and the function only reads its arguments.
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p
           WHERE p.pronamespace = 'public'::regnamespace AND p.proname LIKE 'river\_%'
  LOOP
    EXECUTE format('ALTER FUNCTION %s OWNER TO miniloyal_worker', r.sig);
    EXECUTE format('REVOKE ALL ON FUNCTION %s FROM miniloyal_app, miniloyal_api', r.sig);
  END LOOP;
  -- Standalone types such as the river_job_state enum (not table row types).
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

-- +goose Down
-- Back to the Phase 3 shape: River's objects belong to the schema owner and
-- miniloyal_app may read and write all of them. The cluster-wide
-- miniloyal_worker role stays, like miniloyal_app: other databases on the same
-- cluster may still use it.
-- +goose StatementBegin
DO $$
DECLARE r record;
BEGIN
  IF to_regclass('public.river_job') IS NOT NULL THEN
    DROP TRIGGER IF EXISTS miniloyal_river_job_guard ON river_job;
    REVOKE ALL ON river_job FROM miniloyal_api;
    REVOKE ALL ON SEQUENCE river_job_id_seq FROM miniloyal_api;
  END IF;
  FOR r IN SELECT c.oid::regclass AS rel, c.relkind FROM pg_class c
           WHERE c.relnamespace = 'public'::regnamespace AND c.relname LIKE 'river\_%'
             AND c.relkind IN ('r', 'p', 'S')
  LOOP
    IF r.relkind = 'S' THEN
      EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %s TO miniloyal_app', r.rel);
    ELSE
      EXECUTE format('ALTER TABLE %s OWNER TO %I', r.rel, current_user);
      EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO miniloyal_app', r.rel);
    END IF;
  END LOOP;
  FOR r IN SELECT c.oid::regclass AS rel FROM pg_class c
           WHERE c.relnamespace = 'public'::regnamespace AND c.relname LIKE 'river\_%' AND c.relkind = 'S'
             AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
                             AND d.objid = c.oid AND d.deptype IN ('a', 'i'))
  LOOP
    EXECUTE format('ALTER SEQUENCE %s OWNER TO %I', r.rel, current_user);
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p
           WHERE p.pronamespace = 'public'::regnamespace AND p.proname LIKE 'river\_%'
  LOOP
    EXECUTE format('ALTER FUNCTION %s OWNER TO %I', r.sig, current_user);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO miniloyal_app', r.sig);
  END LOOP;
  FOR r IN SELECT t.oid::regtype AS typ FROM pg_type t
           WHERE t.typnamespace = 'public'::regnamespace AND t.typname LIKE 'river\_%'
             AND t.typrelid = 0
  LOOP
    EXECUTE format('ALTER TYPE %s OWNER TO %I', r.typ, current_user);
  END LOOP;
END
$$;
-- +goose StatementEnd
DROP FUNCTION IF EXISTS miniloyal_secure_river();
DROP FUNCTION IF EXISTS miniloyal_river_job_guard();
