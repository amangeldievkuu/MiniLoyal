-- +goose Up
-- Phase 2: LINE identity. Members (one per LINE user per app), consent records,
-- inbound webhook deduplication, credential verification stamps, team
-- invitations and a per-user security event trail.

-- Proof of control: set when the credentials were verified against LINE.
ALTER TABLE apps ADD COLUMN oa_verified_at timestamptz;
ALTER TABLE app_channels ADD COLUMN verified_at timestamptz;
-- A LINE channel ID may be registered once per environment, so a tenant cannot
-- squat another brand's channel. The three internal channels of one MINI App
-- may share an ID, which is why the environment is part of the key.
DROP INDEX IF EXISTS app_channels_channel_idx;
CREATE UNIQUE INDEX app_channels_channel_env_idx ON app_channels (channel_id, environment);

-- ------------------------------------------------------------------------------------ members
-- A LINE user ID is unique per provider, so members are unique per app, never globally.
-- Erasure (PDPA) anonymizes the row in place; the application has no DELETE grant.
CREATE TABLE members (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id            uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  line_user_id      text NOT NULL,
  display_name      text,
  picture_url       text,
  language          text,
  is_friend         boolean NOT NULL DEFAULT false,
  friend_changed_at timestamptz,
  phone             text,
  birthday          date,
  gender            text CHECK (gender IS NULL OR gender IN ('female', 'male', 'other')),
  custom_fields     jsonb NOT NULL DEFAULT '{}'::jsonb,
  consent_version   text,
  consent_at        timestamptz,
  last_login_at     timestamptz,
  anonymized_at     timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, line_user_id)
);
CREATE INDEX members_app_created_idx ON members (app_id, created_at DESC, id DESC);
CREATE INDEX members_app_name_idx ON members (app_id, lower(display_name));
ALTER TABLE members ENABLE ROW LEVEL SECURITY;
ALTER TABLE members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON members
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE ON members TO miniloyal_app;

-- Append-only record of each consent the member gave, with the text version.
CREATE TABLE member_consents (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  app_id      uuid NOT NULL,
  member_id   uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  version     text NOT NULL,
  accepted_at timestamptz NOT NULL DEFAULT now(),
  ip          inet,
  user_agent  text
);
CREATE INDEX member_consents_member_idx ON member_consents (member_id, id DESC);
ALTER TABLE member_consents ENABLE ROW LEVEL SECURITY;
ALTER TABLE member_consents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON member_consents
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT ON member_consents TO miniloyal_app;

-- LINE redelivers webhooks; each event is processed once per app.
CREATE TABLE webhook_inbound_events (
  tenant_id             uuid NOT NULL,
  app_id                uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  line_webhook_event_id text NOT NULL,
  event_type            text NOT NULL,
  redelivery            boolean NOT NULL DEFAULT false,
  received_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (app_id, line_webhook_event_id)
);
CREATE INDEX webhook_inbound_events_received_idx ON webhook_inbound_events (received_at);
ALTER TABLE webhook_inbound_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_inbound_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_inbound_events
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, DELETE ON webhook_inbound_events TO miniloyal_app;

-- -------------------------------------------------------------------------------- invitations
-- Replaces adding users by email: the invitee proves they own the address by
-- opening the link. Only the token's hash is stored.
CREATE TABLE invitations (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email               citext NOT NULL,
  role                text NOT NULL CHECK (role IN ('owner', 'admin', 'staff')),
  token_hash          text NOT NULL UNIQUE,
  invited_by_user_id  uuid REFERENCES users(id) ON DELETE SET NULL,
  expires_at          timestamptz NOT NULL,
  accepted_at         timestamptz,
  accepted_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  revoked_at          timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX invitations_pending_idx ON invitations (tenant_id, email)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;
ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invitations
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT, UPDATE ON invitations TO miniloyal_app;

-- ---------------------------------------------------------------------------- security events
-- Account-security history shown to the user: sign-ins, password and TOTP
-- changes, sessions ended, refresh replays. User-scoped, append-only.
CREATE TABLE security_events (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  event      text NOT NULL,
  ip         inet,
  user_agent text,
  metadata   jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_events_user_idx ON security_events (user_id, id DESC);
GRANT SELECT, INSERT ON security_events TO miniloyal_app;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO miniloyal_app;

-- +goose Down
DROP TABLE IF EXISTS security_events;
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS webhook_inbound_events;
DROP TABLE IF EXISTS member_consents;
DROP TABLE IF EXISTS members;
DROP INDEX IF EXISTS app_channels_channel_env_idx;
CREATE INDEX app_channels_channel_idx ON app_channels (channel_id);
ALTER TABLE app_channels DROP COLUMN IF EXISTS verified_at;
ALTER TABLE apps DROP COLUMN IF EXISTS oa_verified_at;
