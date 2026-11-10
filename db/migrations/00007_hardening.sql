-- +goose Up
-- Phase 3 security review follow-up (docs/security/phase3-review.md).

-- F3: staff sign in through their own LINE Login channel, so an ID token a
-- staff member's LINE account obtained in the member MINI App cannot open the
-- staff app. The staff channel is never an acceptable member audience.
ALTER TABLE app_channels DROP CONSTRAINT IF EXISTS app_channels_environment_check;
ALTER TABLE app_channels ADD CONSTRAINT app_channels_environment_check
  CHECK (environment IN ('developing', 'review', 'published', 'staff'));

-- F4: a scan token pays for at most one redemption. One row per used token.
CREATE TABLE scan_token_uses (
  jti        text PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id     uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  used_at    timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scan_token_uses_used_idx ON scan_token_uses (used_at);
ALTER TABLE scan_token_uses ENABLE ROW LEVEL SECURITY;
ALTER TABLE scan_token_uses FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON scan_token_uses
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, DELETE ON scan_token_uses TO miniloyal_app;

-- F7: the application never deletes apps, and a delete must never cascade
-- through the append-only points history. Deleting a member or an app that
-- has points now fails instead of erasing the ledger and its lots.
REVOKE DELETE ON apps FROM miniloyal_app;
ALTER TABLE points_ledger DROP CONSTRAINT IF EXISTS points_ledger_member_id_fkey;
ALTER TABLE points_ledger ADD CONSTRAINT points_ledger_member_id_fkey
  FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE RESTRICT;
ALTER TABLE points_ledger ADD CONSTRAINT points_ledger_app_id_fkey
  FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE RESTRICT;
ALTER TABLE points_lots DROP CONSTRAINT IF EXISTS points_lots_member_id_fkey;
ALTER TABLE points_lots ADD CONSTRAINT points_lots_member_id_fkey
  FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE RESTRICT;
ALTER TABLE points_lots DROP CONSTRAINT IF EXISTS points_lots_app_id_fkey;
ALTER TABLE points_lots ADD CONSTRAINT points_lots_app_id_fkey
  FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE RESTRICT;

-- F8: reconciliation details name members and apps of every tenant (the
-- comment in 00006 saying otherwise was wrong). Only system mode, which the
-- worker uses, may read or write the runs.
ALTER TABLE points_reconciliation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE points_reconciliation_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY system_only ON points_reconciliation_runs
  USING (app_is_system())
  WITH CHECK (app_is_system());

-- F6: due lots of one member are found while its account row is locked.
CREATE INDEX IF NOT EXISTS points_lots_member_due_idx ON points_lots (member_id, expires_at)
  WHERE remaining > 0 AND expires_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS points_lots_member_due_idx;
DROP POLICY IF EXISTS system_only ON points_reconciliation_runs;
ALTER TABLE points_reconciliation_runs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE points_reconciliation_runs DISABLE ROW LEVEL SECURITY;
ALTER TABLE points_lots DROP CONSTRAINT IF EXISTS points_lots_app_id_fkey;
ALTER TABLE points_lots ADD CONSTRAINT points_lots_app_id_fkey
  FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE;
ALTER TABLE points_lots DROP CONSTRAINT IF EXISTS points_lots_member_id_fkey;
ALTER TABLE points_lots ADD CONSTRAINT points_lots_member_id_fkey
  FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE CASCADE;
ALTER TABLE points_ledger DROP CONSTRAINT IF EXISTS points_ledger_app_id_fkey;
ALTER TABLE points_ledger DROP CONSTRAINT IF EXISTS points_ledger_member_id_fkey;
ALTER TABLE points_ledger ADD CONSTRAINT points_ledger_member_id_fkey
  FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE CASCADE;
GRANT DELETE ON apps TO miniloyal_app;
DROP TABLE IF EXISTS scan_token_uses;
ALTER TABLE app_channels DROP CONSTRAINT IF EXISTS app_channels_environment_check;
ALTER TABLE app_channels ADD CONSTRAINT app_channels_environment_check
  CHECK (environment IN ('developing', 'review', 'published'));
