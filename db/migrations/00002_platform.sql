-- +goose Up
-- Platform core: tenants, dashboard users and sessions, apps and their LINE channels, stores,
-- staff, API keys, audit log and idempotency keys.
--
-- Conventions (see .claude/rules/sql.md):
--   * every tenant-scoped row carries tenant_id (and app_id for app-scoped data);
--   * RLS is enabled and forced; policies read app_tenant_id() which the application sets per
--     transaction with set_config('app.tenant_id', ..., true);
--   * app_is_system() is true only inside internal/platform/db.WithSystem, for lookups that run
--     before the tenant is known (sign-in, API key lookup, webhook routing);
--   * the application connects as miniloyal_api, a member of miniloyal_app, never as the owner.
--     "migrate up" creates both roles before applying migrations.

GRANT USAGE ON SCHEMA public TO miniloyal_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app_tenant_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app_is_system() RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT current_setting('app.system', true) = 'on'
$$;
-- +goose StatementEnd

-- ------------------------------------------------------------------------------------ tenants
CREATE TABLE tenants (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  type             text NOT NULL CHECK (type IN ('brand', 'agency')),
  name             text NOT NULL,
  slug             citext NOT NULL UNIQUE,
  plan             text NOT NULL DEFAULT 'free',
  parent_tenant_id uuid REFERENCES tenants(id),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants
  USING (app_is_system() OR id = app_tenant_id() OR parent_tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE ON tenants TO miniloyal_app;

-- Dashboard identities are global; a user joins tenants through tenant_users.
CREATE TABLE users (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email               citext NOT NULL UNIQUE,
  password_hash       text NOT NULL,
  display_name        text,
  -- Envelope-encrypted TOTP secret: pending until totp_enabled_at is set.
  totp_secret_enc     bytea,
  totp_enabled_at     timestamptz,
  -- Access tokens issued before this instant are refused (password change, sign out everywhere).
  sessions_valid_from timestamptz,
  last_login_at       timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT, UPDATE, DELETE ON users TO miniloyal_app;

CREATE TABLE tenant_users (
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('owner', 'admin', 'staff')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, user_id)
);
CREATE INDEX tenant_users_user_idx ON tenant_users (user_id);
ALTER TABLE tenant_users ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_users
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_users TO miniloyal_app;

-- Rotating refresh tokens, stored as hashes. family groups a rotation chain; parent_id tells a
-- replacement the client never received from a replayed stolen token.
CREATE TABLE refresh_tokens (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  family     uuid NOT NULL,
  parent_id  uuid REFERENCES refresh_tokens(id) ON DELETE SET NULL,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  user_agent text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family);
CREATE INDEX refresh_tokens_parent_idx ON refresh_tokens (parent_id);
GRANT SELECT, INSERT, UPDATE, DELETE ON refresh_tokens TO miniloyal_app;

CREATE TABLE totp_recovery_codes (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  text NOT NULL,
  used_at    timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, code_hash)
);
GRANT SELECT, INSERT, UPDATE, DELETE ON totp_recovery_codes TO miniloyal_app;

-- --------------------------------------------------------------------------------------- apps
-- One app is one LINE MINI App. The brand's LINE Official Account (Messaging API channel) hangs
-- off the app; its channel secret is envelope-encrypted.
CREATE TABLE apps (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name                  text NOT NULL,
  slug                  citext NOT NULL,
  locale                text NOT NULL DEFAULT 'zh-TW',
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  oa_channel_id         text,
  oa_basic_id           text,
  oa_channel_secret_enc bytea,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, slug)
);
CREATE INDEX apps_tenant_idx ON apps (tenant_id);
-- Webhooks are routed by the OA channel, so one OA belongs to one app.
CREATE UNIQUE INDEX apps_oa_channel_idx ON apps (oa_channel_id) WHERE oa_channel_id IS NOT NULL;
ALTER TABLE apps ENABLE ROW LEVEL SECURITY;
ALTER TABLE apps FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON apps
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON apps TO miniloyal_app;

-- The three internal channels of a LINE MINI App channel (developing, review, published). Each
-- has its own LIFF ID; the channel ID is the ID token audience.
CREATE TABLE app_channels (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id             uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  environment        text NOT NULL CHECK (environment IN ('developing', 'review', 'published')),
  channel_id         text NOT NULL,
  liff_id            text NOT NULL,
  channel_secret_enc bytea,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, environment)
);
CREATE INDEX app_channels_channel_idx ON app_channels (channel_id);
CREATE UNIQUE INDEX app_channels_liff_idx ON app_channels (liff_id);
ALTER TABLE app_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON app_channels
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON app_channels TO miniloyal_app;

-- ------------------------------------------------------------------------------------- stores
CREATE TABLE stores (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id     uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  name       text NOT NULL,
  code       text,
  address    text,
  status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX stores_app_idx ON stores (app_id);
CREATE UNIQUE INDEX stores_code_idx ON stores (app_id, code) WHERE code IS NOT NULL;
ALTER TABLE stores ENABLE ROW LEVEL SECURITY;
ALTER TABLE stores FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON stores
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON stores TO miniloyal_app;

-- -------------------------------------------------------------------------------------- staff
-- Staff bind their LINE account in the staff app with a one-time invite code (hashed here).
CREATE TABLE staff (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id            uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  store_id          uuid REFERENCES stores(id) ON DELETE RESTRICT,
  display_name      text NOT NULL,
  role              text NOT NULL DEFAULT 'cashier' CHECK (role IN ('cashier', 'manager')),
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  line_user_id      text,
  invite_code_hash  text,
  invite_expires_at timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX staff_app_idx ON staff (app_id);
CREATE INDEX staff_store_idx ON staff (store_id);
CREATE UNIQUE INDEX staff_line_user_idx ON staff (app_id, line_user_id) WHERE line_user_id IS NOT NULL;
CREATE UNIQUE INDEX staff_invite_idx ON staff (invite_code_hash) WHERE invite_code_hash IS NOT NULL;
ALTER TABLE staff ENABLE ROW LEVEL SECURITY;
ALTER TABLE staff FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON staff
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON staff TO miniloyal_app;

-- ----------------------------------------------------------------------------------- api keys
-- Only the SHA-256 of a key is stored; prefix lets people recognise a key in lists.
CREATE TABLE api_keys (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id             uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  name               text NOT NULL,
  prefix             text NOT NULL,
  key_hash           text NOT NULL UNIQUE,
  scopes             text[] NOT NULL,
  last_used_at       timestamptz,
  revoked_at         timestamptz,
  created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_app_idx ON api_keys (app_id) WHERE revoked_at IS NULL;
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_keys
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE ON api_keys TO miniloyal_app;

-- ---------------------------------------------------------------------------------- audit log
-- Append-only: the application may insert and read, never change or delete.
CREATE TABLE audit_log (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  app_id      uuid,
  actor_type  text NOT NULL CHECK (actor_type IN ('user', 'staff', 'api_key', 'system')),
  actor_id    uuid,
  action      text NOT NULL,
  target_type text NOT NULL,
  target_id   text,
  metadata    jsonb NOT NULL DEFAULT '{}'::jsonb,
  request_id  text,
  ip          inet,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_tenant_idx ON audit_log (tenant_id, id DESC);
CREATE INDEX audit_log_app_idx ON audit_log (app_id, id DESC) WHERE app_id IS NOT NULL;
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT ON audit_log TO miniloyal_app;

-- --------------------------------------------------------------------------- idempotency keys
-- One row per (tenant, principal scope, key). status_code is NULL while the first request is in
-- flight; a retry with the same request_hash replays the stored response, a different hash is
-- answered 422.
CREATE TABLE idempotency_keys (
  tenant_id     uuid NOT NULL,
  scope         text NOT NULL,
  key           text NOT NULL,
  request_hash  bytea NOT NULL,
  status_code   integer,
  response_body jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, scope, key)
);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys (expires_at);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_keys
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO miniloyal_app;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO miniloyal_app;

-- +goose Down
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS staff;
DROP TABLE IF EXISTS stores;
DROP TABLE IF EXISTS app_channels;
DROP TABLE IF EXISTS apps;
DROP TABLE IF EXISTS totp_recovery_codes;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS tenant_users;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;
DROP FUNCTION IF EXISTS app_is_system();
DROP FUNCTION IF EXISTS app_tenant_id();
REVOKE USAGE ON SCHEMA public FROM miniloyal_app;
