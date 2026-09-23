-- 0002_identity — users, admins, roles, permissions and the audit foundation.
--
-- Owned by Phase 1. The table shapes follow `db/schema.sql`, which is the
-- consolidated reference for the target schema; the additions here are
-- constraints and indexes, which the reference leaves implicit.
--
-- Two vocabularies are defined here because the specifications fix neither:
--
--   users.status / admins.status  active | suspended
--   audit_events.actor_type       user | admin | system | provider
--
-- `suspended` is the word `docs/15` uses for the `users.suspend` permission. The
-- actor types mirror the isolation `docs/14` requires between users, admins and
-- provider credentials, plus `system` for work performed by the platform itself
-- (the reconciler, the outbox relay). Both are enforced with CHECK constraints so
-- a new value has to be a deliberate change to this file rather than a typo that
-- silently creates a state nothing handles.
--
-- `email` is stored already normalised (trimmed and lower-cased) by the
-- application, and the UNIQUE constraint is on the stored value exactly as the
-- reference declares. The alternative — a unique index on `lower(email)` — would
-- put the guarantee in the database, but it would also make this table differ
-- from the reference that the rest of the roadmap is written against. The
-- normalisation therefore lives at the single choke point that writes users, and
-- is covered by a test.
--
-- `updated_at` is maintained by the application rather than by a trigger: the
-- write path already knows it is updating, and a trigger would also fire for the
-- maintenance and seeding work where the timestamp is not meaningful.

CREATE TABLE users (
  id uuid PRIMARY KEY,
  email varchar(320) NOT NULL UNIQUE,
  password_hash text NOT NULL,
  status varchar(64) NOT NULL,
  locale varchar(16) NOT NULL DEFAULT 'zh-CN',
  timezone varchar(64) NOT NULL DEFAULT 'UTC',
  email_verified_at timestamptz,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT users_status_known CHECK (status IN ('active', 'suspended')),
  -- docs/13 defines exactly two locales; a third would render as raw keys.
  CONSTRAINT users_locale_supported CHECK (locale IN ('zh-CN', 'en-US'))
);

CREATE TABLE admins (
  id uuid PRIMARY KEY,
  email varchar(320) NOT NULL UNIQUE,
  password_hash text NOT NULL,
  status varchar(64) NOT NULL,
  display_name varchar(255),
  -- Carried from Phase 1 so the field is not introduced alongside the flow that
  -- uses it. Admin 2FA is phase 12 work (TASKS.md); the column exists and is
  -- reported so the gap is visible rather than implied.
  two_factor_enabled boolean NOT NULL DEFAULT false,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT admins_status_known CHECK (status IN ('active', 'suspended'))
);

-- `name_key` is an i18n key rather than a display name, so role names are
-- translated in the interface instead of being stored in one language
-- (docs/13, docs/15).
CREATE TABLE roles (
  id uuid PRIMARY KEY,
  key varchar(128) NOT NULL UNIQUE,
  name_key varchar(255) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE permissions (
  id uuid PRIMARY KEY,
  key varchar(255) NOT NULL UNIQUE
);

CREATE TABLE admin_roles (
  admin_id uuid NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  role_id uuid NOT NULL REFERENCES roles(id),
  PRIMARY KEY (admin_id, role_id)
);

CREATE TABLE role_permissions (
  role_id uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_id uuid NOT NULL REFERENCES permissions(id),
  PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE audit_events (
  id uuid PRIMARY KEY,
  actor_type varchar(32) NOT NULL,
  actor_id uuid,
  action varchar(255) NOT NULL,
  resource_type varchar(64) NOT NULL,
  resource_id uuid,
  before_data jsonb,
  after_data jsonb,
  ip_address inet,
  user_agent text,
  request_id varchar(255),
  trace_id varchar(255),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT audit_events_actor_type_known
    CHECK (actor_type IN ('user', 'admin', 'system', 'provider'))
);

-- The primary keys above already index the forward direction of each join. The
-- reverse direction is indexed because both questions are asked: "which admins
-- hold this role" when a role's permissions change, and "which roles grant this
-- permission" when a permission is revoked.
CREATE INDEX admin_roles_role_id_idx ON admin_roles (role_id);
CREATE INDEX role_permissions_permission_id_idx ON role_permissions (permission_id);

-- The audit screen (Phase 9) pages by time, filters by resource, and looks up
-- everything one actor did. Audit is append-only and grows without bound, so the
-- indexes exist from the start rather than being added when the table is large.
CREATE INDEX audit_events_created_at_idx ON audit_events (created_at DESC);
CREATE INDEX audit_events_resource_idx ON audit_events (resource_type, resource_id, created_at DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_type, actor_id, created_at DESC);
