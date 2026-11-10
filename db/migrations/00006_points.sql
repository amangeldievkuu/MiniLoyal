-- +goose Up
-- Phase 3: points (rules, accounts, FIFO lots, append-only ledger), the
-- rotating member card, staff sign-in, and the security items deferred from
-- the Phase 2 review.

-- HS256 ID tokens (LINE web login) are opt-in per app; LIFF issues ES256.
ALTER TABLE apps ADD COLUMN allow_hs256_id_tokens boolean NOT NULL DEFAULT false;
-- Member sessions issued before this instant are refused (sign every member out).
ALTER TABLE apps ADD COLUMN member_sessions_valid_from timestamptz;

-- Bumping qr_key_version invalidates every QR code the member's phone shows.
ALTER TABLE members ADD COLUMN qr_key_version integer NOT NULL DEFAULT 1;

-- Staff sign in through LINE in the staff app; invite codes are matched within
-- one app only, and staff sessions can be ended.
DROP INDEX IF EXISTS staff_invite_idx;
CREATE UNIQUE INDEX staff_invite_idx ON staff (app_id, invite_code_hash) WHERE invite_code_hash IS NOT NULL;
ALTER TABLE staff ADD COLUMN sessions_valid_from timestamptz;
ALTER TABLE staff ADD COLUMN last_login_at timestamptz;

-- ------------------------------------------------------------------------------- points rules
-- One rule set per app. spend_per_point: currency units spent per points_per_unit
-- points (NT$100 = 1 point is spend_per_point 100, points_per_unit 1).
CREATE TABLE points_rules (
  app_id               uuid PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  currency             text NOT NULL DEFAULT 'TWD',
  spend_per_point      integer NOT NULL DEFAULT 100 CHECK (spend_per_point > 0),
  points_per_unit      integer NOT NULL DEFAULT 1 CHECK (points_per_unit > 0),
  min_spend            integer NOT NULL DEFAULT 0 CHECK (min_spend >= 0),
  max_points_per_txn   integer CHECK (max_points_per_txn IS NULL OR max_points_per_txn > 0),
  expiry_days          integer CHECK (expiry_days IS NULL OR expiry_days > 0),
  enabled              boolean NOT NULL DEFAULT true,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE points_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE points_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON points_rules
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE ON points_rules TO miniloyal_app;

-- ----------------------------------------------------------------------------- points accounts
-- The cached balance. Every change locks this row (SELECT ... FOR UPDATE) first.
CREATE TABLE points_accounts (
  member_id        uuid PRIMARY KEY REFERENCES members(id) ON DELETE CASCADE,
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id           uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  balance          bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
  lifetime_earned  bigint NOT NULL DEFAULT 0 CHECK (lifetime_earned >= 0),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX points_accounts_app_idx ON points_accounts (app_id);
ALTER TABLE points_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE points_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON points_accounts
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE ON points_accounts TO miniloyal_app;

-- -------------------------------------------------------------------------------- points lots
-- Each earn creates a lot; redemptions and expiry consume lots FIFO
-- (ORDER BY expires_at NULLS LAST, created_at, id). Only remaining changes.
CREATE TABLE points_lots (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id      uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  member_id   uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  earned      integer NOT NULL CHECK (earned > 0),
  remaining   integer NOT NULL CHECK (remaining >= 0 AND remaining <= earned),
  expires_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX points_lots_open_idx ON points_lots (member_id, expires_at NULLS LAST, created_at, id) WHERE remaining > 0;
CREATE INDEX points_lots_expiry_idx ON points_lots (expires_at) WHERE remaining > 0 AND expires_at IS NOT NULL;
ALTER TABLE points_lots ENABLE ROW LEVEL SECURITY;
ALTER TABLE points_lots FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON points_lots
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE (remaining) ON points_lots TO miniloyal_app;

-- ------------------------------------------------------------------------------ points ledger
-- Append-only. One row per lot touched; rows of one operation share txn_id.
-- balance_after is the account balance once the whole transaction applied.
CREATE TABLE points_ledger (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id       uuid NOT NULL,
  app_id          uuid NOT NULL,
  member_id       uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  txn_id          uuid NOT NULL,
  lot_id          bigint REFERENCES points_lots(id),
  delta           integer NOT NULL CHECK (delta <> 0),
  balance_after   bigint NOT NULL CHECK (balance_after >= 0),
  reason          text NOT NULL CHECK (reason IN ('earn', 'redeem', 'adjust', 'expire')),
  spend_amount    integer CHECK (spend_amount IS NULL OR spend_amount >= 0),
  ref_type        text,
  ref_id          text,
  note            text,
  store_id        uuid,
  actor_type      text NOT NULL CHECK (actor_type IN ('member', 'staff', 'user', 'api_key', 'system')),
  actor_id        uuid,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX points_ledger_member_idx ON points_ledger (member_id, id DESC);
CREATE INDEX points_ledger_txn_idx ON points_ledger (txn_id);
CREATE INDEX points_ledger_app_idx ON points_ledger (app_id, id DESC);
-- A lot expires once; a retried expiry job cannot expire it twice.
CREATE UNIQUE INDEX points_ledger_expire_once_idx ON points_ledger (lot_id) WHERE reason = 'expire';
ALTER TABLE points_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE points_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON points_ledger
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT ON points_ledger TO miniloyal_app;

-- ------------------------------------------------------------------------ reconciliation runs
-- Platform-level results of the daily check (ledger sum = cached balance = open lots).
-- Only the worker writes it, in system mode; it carries no member data.
CREATE TABLE points_reconciliation_runs (
  id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  started_at        timestamptz NOT NULL,
  finished_at       timestamptz,
  accounts_checked  integer NOT NULL DEFAULT 0,
  mismatches        integer NOT NULL DEFAULT 0,
  details           jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at        timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT, UPDATE ON points_reconciliation_runs TO miniloyal_app;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO miniloyal_app;

-- +goose Down
DROP TABLE IF EXISTS points_reconciliation_runs;
DROP TABLE IF EXISTS points_ledger;
DROP TABLE IF EXISTS points_lots;
DROP TABLE IF EXISTS points_accounts;
DROP TABLE IF EXISTS points_rules;
ALTER TABLE staff DROP COLUMN IF EXISTS last_login_at;
ALTER TABLE staff DROP COLUMN IF EXISTS sessions_valid_from;
DROP INDEX IF EXISTS staff_invite_idx;
CREATE UNIQUE INDEX staff_invite_idx ON staff (invite_code_hash) WHERE invite_code_hash IS NOT NULL;
ALTER TABLE members DROP COLUMN IF EXISTS qr_key_version;
ALTER TABLE apps DROP COLUMN IF EXISTS member_sessions_valid_from;
ALTER TABLE apps DROP COLUMN IF EXISTS allow_hs256_id_tokens;
