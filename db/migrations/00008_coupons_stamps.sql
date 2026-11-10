-- +goose Up
-- Phase 4: coupon templates and instances, stamp cards with append-only
-- events, and the outbound event outbox.
--
-- Convention: every table carries tenant_id and app_id; RLS is enabled and
-- forced with the standard tenant policy. Business times (issued, expires,
-- redeemed, voided, occurred) come from the service clock, never now().
-- Composite foreign keys on (app_id, id) keep every reference inside one app,
-- so a row can never point at another app's member, template or card.

-- Foreign key checks bypass RLS, so without these a tenant could write a row
-- under its own tenant_id that points at another tenant's app or member.
-- (tenant_id, app_id) must name a real app of that tenant, and members are
-- referenced by (app_id, id).
ALTER TABLE apps ADD CONSTRAINT apps_tenant_id_id_key UNIQUE (tenant_id, id);
ALTER TABLE members ADD CONSTRAINT members_app_id_id_key UNIQUE (app_id, id);

-- ---------------------------------------------------------------------------- coupon templates
-- Templates are archived, never deleted, because instances reference them.
-- claimed_count counts issued instances of every source (claim, issue and
-- stamp_reward) and is incremented under SELECT ... FOR UPDATE on this row.
CREATE TABLE coupon_templates (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id            uuid NOT NULL,
  name              text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description       text CHECK (description IS NULL OR char_length(description) <= 2000),
  type              text NOT NULL CHECK (type IN ('amount_off', 'percent_off', 'free_item')),
  -- amount_off: whole currency units; percent_off: 1..100; free_item: none.
  value             integer,
  total_limit       integer CHECK (total_limit IS NULL OR total_limit > 0),
  per_member_limit  integer CHECK (per_member_limit IS NULL OR per_member_limit > 0),
  valid_from        timestamptz,
  valid_to          timestamptz,
  -- Days an instance stays valid after issue; valid_to caps it when both are set.
  validity_days     integer CHECK (validity_days IS NULL OR validity_days BETWEEN 1 AND 3650),
  -- Members may claim it themselves. False for reward and issue-only templates.
  claimable         boolean NOT NULL DEFAULT true,
  claimed_count     integer NOT NULL DEFAULT 0 CHECK (claimed_count >= 0),
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, app_id) REFERENCES apps (tenant_id, id) ON DELETE CASCADE,
  UNIQUE (app_id, id),
  -- value IS NOT NULL is explicit: a NULL comparison would pass the CHECK.
  CONSTRAINT coupon_templates_value_check CHECK (
    (type = 'amount_off' AND value IS NOT NULL AND value BETWEEN 1 AND 1000000) OR
    (type = 'percent_off' AND value IS NOT NULL AND value BETWEEN 1 AND 100) OR
    (type = 'free_item' AND value IS NULL)
  ),
  CONSTRAINT coupon_templates_window_check CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_to > valid_from),
  CONSTRAINT coupon_templates_limit_check CHECK (total_limit IS NULL OR claimed_count <= total_limit),
  CONSTRAINT coupon_templates_per_member_check CHECK (total_limit IS NULL OR per_member_limit IS NULL OR per_member_limit <= total_limit)
);
CREATE INDEX coupon_templates_app_idx ON coupon_templates (app_id, status, created_at DESC, id DESC);
ALTER TABLE coupon_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE coupon_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coupon_templates
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
-- No DELETE: archive instead. tenant_id, app_id and type never change.
GRANT SELECT, INSERT ON coupon_templates TO miniloyal_app;
GRANT UPDATE (name, description, value, total_limit, per_member_limit, valid_from, valid_to,
  validity_days, claimable, claimed_count, status, updated_at) ON coupon_templates TO miniloyal_app;

-- ---------------------------------------------------------------------------- coupon instances
-- One row per coupon a member holds. Expiry is computed from expires_at (set at
-- issue from valid_to or issued_at + validity_days), so no job flips a status.
-- Redeem is one atomic UPDATE ... WHERE status = 'issued' AND not expired.
-- Idempotency lives in idempotency_keys, as for points; txn_id groups an
-- instance with the stamp events or outbound events of the same operation.
-- staff and store IDs carry no foreign key, like points_ledger: staff and
-- stores can be deleted, and the redemption record must survive that.
CREATE TABLE coupon_instances (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL,
  app_id                uuid NOT NULL,
  template_id           uuid NOT NULL,
  member_id             uuid NOT NULL,
  -- Short code printed on receipts and read out at the counter, unique per app.
  code                  text NOT NULL CHECK (code ~ '^[0-9A-Z]{6,12}$'),
  status                text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued', 'redeemed', 'voided')),
  source                text NOT NULL CHECK (source IN ('claim', 'issue', 'stamp_reward')),
  txn_id                uuid NOT NULL,
  issued_at             timestamptz NOT NULL,
  expires_at            timestamptz,
  issued_by_type        text NOT NULL CHECK (issued_by_type IN ('member', 'staff', 'user', 'api_key', 'system')),
  issued_by_id          uuid,
  redeemed_at           timestamptz,
  redeemed_by_staff_id  uuid,
  redeemed_at_store_id  uuid,
  voided_at             timestamptz,
  voided_by_type        text CHECK (voided_by_type IS NULL OR voided_by_type IN ('staff', 'user', 'api_key', 'system')),
  voided_by_id          uuid,
  void_reason           text CHECK (void_reason IS NULL OR char_length(void_reason) <= 500),
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, app_id) REFERENCES apps (tenant_id, id) ON DELETE RESTRICT,
  UNIQUE (app_id, code),
  FOREIGN KEY (app_id, template_id) REFERENCES coupon_templates (app_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (app_id, member_id) REFERENCES members (app_id, id) ON DELETE RESTRICT,
  CONSTRAINT coupon_instances_expiry_check CHECK (expires_at IS NULL OR expires_at > issued_at),
  CONSTRAINT coupon_instances_state_check CHECK (
    (status = 'issued' AND redeemed_at IS NULL AND voided_at IS NULL) OR
    (status = 'redeemed' AND redeemed_at IS NOT NULL AND voided_at IS NULL) OR
    (status = 'voided' AND voided_at IS NOT NULL AND voided_by_type IS NOT NULL AND redeemed_at IS NULL)
  )
);
-- A member's wallet, filtered by status.
CREATE INDEX coupon_instances_member_idx ON coupon_instances (app_id, member_id, status, issued_at DESC);
-- Per-member limit counting under the template lock.
CREATE INDEX coupon_instances_template_member_idx ON coupon_instances (app_id, template_id, member_id);
CREATE INDEX coupon_instances_txn_idx ON coupon_instances (app_id, txn_id);
ALTER TABLE coupon_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE coupon_instances FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coupon_instances
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
-- No DELETE: an instance is voided. Only the redeem and void columns change.
GRANT SELECT, INSERT ON coupon_instances TO miniloyal_app;
GRANT UPDATE (status, redeemed_at, redeemed_by_staff_id, redeemed_at_store_id,
  voided_at, voided_by_type, voided_by_id, void_reason, updated_at) ON coupon_instances TO miniloyal_app;

-- -------------------------------------------------------------------------------- stamp cards
CREATE TABLE stamp_cards (
  id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  app_id                     uuid NOT NULL,
  name                       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description                text CHECK (description IS NULL OR char_length(description) <= 2000),
  stamps_required            integer NOT NULL CHECK (stamps_required BETWEEN 1 AND 100),
  -- Issued with source stamp_reward on completion; must belong to the same app.
  reward_coupon_template_id  uuid NOT NULL,
  max_stamps_per_visit       integer NOT NULL DEFAULT 1 CHECK (max_stamps_per_visit BETWEEN 1 AND 100),
  status                     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  created_at                 timestamptz NOT NULL DEFAULT now(),
  updated_at                 timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, app_id) REFERENCES apps (tenant_id, id) ON DELETE CASCADE,
  UNIQUE (app_id, id),
  -- NO ACTION, not RESTRICT: deleting an app cascades through cards and templates together.
  FOREIGN KEY (app_id, reward_coupon_template_id) REFERENCES coupon_templates (app_id, id)
);
CREATE INDEX stamp_cards_app_idx ON stamp_cards (app_id, status, created_at DESC, id DESC);
ALTER TABLE stamp_cards ENABLE ROW LEVEL SECURITY;
ALTER TABLE stamp_cards FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON stamp_cards
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
-- No DELETE: archive instead, since progress and events reference the card.
GRANT SELECT, INSERT ON stamp_cards TO miniloyal_app;
GRANT UPDATE (name, description, stamps_required, reward_coupon_template_id,
  max_stamps_per_visit, status, updated_at) ON stamp_cards TO miniloyal_app;

-- ----------------------------------------------------------------------------- stamp progress
-- The cached state of one member on one card. Every stamp locks this row
-- (SELECT ... FOR UPDATE) before it writes stamp_events.
CREATE TABLE stamp_progress (
  member_id      uuid NOT NULL,
  stamp_card_id  uuid NOT NULL,
  tenant_id      uuid NOT NULL,
  app_id         uuid NOT NULL,
  count          integer NOT NULL DEFAULT 0 CHECK (count >= 0),
  completions    integer NOT NULL DEFAULT 0 CHECK (completions >= 0),
  last_stamped_at timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, app_id) REFERENCES apps (tenant_id, id) ON DELETE RESTRICT,
  PRIMARY KEY (member_id, stamp_card_id),
  FOREIGN KEY (app_id, member_id) REFERENCES members (app_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (app_id, stamp_card_id) REFERENCES stamp_cards (app_id, id) ON DELETE RESTRICT
);
CREATE INDEX stamp_progress_member_idx ON stamp_progress (app_id, member_id);
CREATE INDEX stamp_progress_card_idx ON stamp_progress (app_id, stamp_card_id);
ALTER TABLE stamp_progress ENABLE ROW LEVEL SECURITY;
ALTER TABLE stamp_progress FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON stamp_progress
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT ON stamp_progress TO miniloyal_app;
GRANT UPDATE (count, completions, last_stamped_at, updated_at) ON stamp_progress TO miniloyal_app;

-- ------------------------------------------------------------------------------- stamp events
-- Append-only. Rows of one operation share txn_id. A stamp that fills the card
-- writes a positive 'stamp' row, then a 'completion' row with delta
-- -stamps_required and the reward coupon instance. count_after and
-- completions_after are the progress once the row applied.
CREATE TABLE stamp_events (
  id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id           uuid NOT NULL,
  app_id              uuid NOT NULL,
  member_id           uuid NOT NULL,
  stamp_card_id       uuid NOT NULL,
  txn_id              uuid NOT NULL,
  delta               integer NOT NULL CHECK (delta <> 0),
  count_after         integer NOT NULL CHECK (count_after >= 0),
  completions_after   integer NOT NULL CHECK (completions_after >= 0),
  reason              text NOT NULL CHECK (reason IN ('stamp', 'adjust', 'completion')),
  coupon_instance_id  uuid REFERENCES coupon_instances(id) ON DELETE RESTRICT,
  note                text CHECK (note IS NULL OR char_length(note) <= 500),
  staff_id            uuid,
  store_id            uuid,
  actor_type          text NOT NULL CHECK (actor_type IN ('member', 'staff', 'user', 'api_key', 'system')),
  actor_id            uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, app_id) REFERENCES apps (tenant_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (app_id, member_id) REFERENCES members (app_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (app_id, stamp_card_id) REFERENCES stamp_cards (app_id, id) ON DELETE RESTRICT,
  CONSTRAINT stamp_events_reason_shape_check CHECK (
    (reason = 'stamp' AND delta > 0 AND coupon_instance_id IS NULL) OR
    (reason = 'adjust' AND coupon_instance_id IS NULL) OR
    (reason = 'completion' AND delta < 0 AND coupon_instance_id IS NOT NULL)
  )
);
CREATE INDEX stamp_events_member_idx ON stamp_events (app_id, member_id, id DESC);
CREATE INDEX stamp_events_card_member_idx ON stamp_events (app_id, stamp_card_id, member_id, id DESC);
-- One reward instance pays for one completion, and a card completes once per
-- completion number, so a retried or racing stamp can never double a reward.
CREATE UNIQUE INDEX stamp_events_reward_once_idx ON stamp_events (coupon_instance_id) WHERE coupon_instance_id IS NOT NULL;
CREATE UNIQUE INDEX stamp_events_completion_once_idx ON stamp_events (app_id, stamp_card_id, member_id, completions_after)
  WHERE reason = 'completion';
ALTER TABLE stamp_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE stamp_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON stamp_events
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT ON stamp_events TO miniloyal_app; -- append-only: no UPDATE or DELETE

-- ---------------------------------------------------------------------------- outbound events
-- Append-only outbox, written in the same transaction as the change it
-- describes. Delivery state (endpoints, attempts, signatures) lives in Phase 5
-- tables, so these rows never change. Payloads carry IDs and amounts, not
-- personal data: erasure cannot blank an append-only row.
CREATE TABLE outbound_events (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  -- Public, stable event ID sent to customers for their own deduplication.
  event_id     uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  tenant_id    uuid NOT NULL,
  app_id       uuid NOT NULL,
  type         text NOT NULL CHECK (type IN (
                 'member.created', 'member.updated', 'friend.added', 'friend.removed',
                 'points.changed', 'coupon.issued', 'coupon.redeemed', 'stamp.completed')),
  member_id    uuid,
  payload      jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
  occurred_at  timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, app_id) REFERENCES apps (tenant_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (app_id, member_id) REFERENCES members (app_id, id) ON DELETE RESTRICT
);
-- Keyset on id: identity order is the order rows were written, created_at is
-- only the transaction start.
CREATE INDEX outbound_events_app_idx ON outbound_events (app_id, id);
CREATE INDEX outbound_events_member_idx ON outbound_events (app_id, member_id, id) WHERE member_id IS NOT NULL;
ALTER TABLE outbound_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbound_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON outbound_events
  USING (app_is_system() OR tenant_id = app_tenant_id())
  WITH CHECK (app_is_system() OR tenant_id = app_tenant_id());
GRANT SELECT, INSERT ON outbound_events TO miniloyal_app; -- append-only: no UPDATE or DELETE

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO miniloyal_app;

-- +goose Down
DROP TABLE IF EXISTS outbound_events;
DROP TABLE IF EXISTS stamp_events;
DROP TABLE IF EXISTS stamp_progress;
DROP TABLE IF EXISTS stamp_cards;
DROP TABLE IF EXISTS coupon_instances;
DROP TABLE IF EXISTS coupon_templates;
ALTER TABLE members DROP CONSTRAINT IF EXISTS members_app_id_id_key;
ALTER TABLE apps DROP CONSTRAINT IF EXISTS apps_tenant_id_id_key;
