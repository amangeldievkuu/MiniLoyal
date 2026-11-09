-- +goose Up
-- Security review follow-up (Phase 1): single-use MFA challenges with capped
-- attempts, and TOTP step tracking so an observed code cannot be replayed.

ALTER TABLE users ADD COLUMN totp_last_step bigint;

-- One row per MFA challenge token (its jti). The password step creates it;
-- each code attempt increments attempts; success marks it used.
CREATE TABLE mfa_challenges (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  attempts   integer NOT NULL DEFAULT 0,
  used_at    timestamptz,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_challenges_expires_idx ON mfa_challenges (expires_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON mfa_challenges TO miniloyal_app;

-- +goose Down
DROP TABLE IF EXISTS mfa_challenges;
ALTER TABLE users DROP COLUMN IF EXISTS totp_last_step;
