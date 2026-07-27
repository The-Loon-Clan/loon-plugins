-- Entitlements Stage 2.0 — the groups model, in this plugin's own schema.
--
-- Runs under `SET LOCAL search_path = "ranks", public` (loon's plugin migration
-- runner), so unqualified CREATEs land in the plugin schema while the seed's
-- reads of user_ranks / user_rank_subscriptions resolve to public. That is what
-- lets the plugin own its tables today without the host losing anything: the
-- legacy public tables stay authoritative for all 8 existing readers and are
-- kept in step by the plugin's dual-write until Stage 3 rewires them.
--
-- ADDITIVE AND INERT. When this ships, nothing reads or writes these tables —
-- an old binary and a new one behave identically.
--
-- Seed volume against live prod is 5 catalog rows, 12 entitlement rows and 2
-- memberships, so the copy runs inline. The 2026-07-11 "never block boot on a
-- backfill" lesson is about a per-row COUNT over 850k nzbs, not 19 rows; the
-- plugin still ships a reconcile entry point as the standard replay seam.

-- ── Catalog ──────────────────────────────────────────────────────────────────
-- No download_limit / api_limit columns: limits become entitlement rows in
-- group_entitlements. Splitting "what you may do" from "what you are called" is
-- the entire point of the stage. Dual-write derives the legacy columns back out
-- of the download.daily / api.daily keys so legacy readers see no change.
CREATE TABLE IF NOT EXISTS groups (
    id            SERIAL      PRIMARY KEY,
    slug          TEXT        NOT NULL UNIQUE,
    -- Byte-stable: 7 live discord_role_<lowercased name> settings keys are
    -- name-addressed, so renaming a group silently breaks Discord role sync
    -- until that is re-keyed to slug (Stage 3).
    name          TEXT        NOT NULL UNIQUE,
    kind          TEXT        NOT NULL DEFAULT 'paid'
                              CHECK (kind IN ('paid', 'earned', 'assigned')),
    -- visible=false grants entitlements but shows NO badge anywhere.
    -- GroupDisplay filters on it, and dual-write skips hidden groups entirely,
    -- so an invisible group can never leak into a legacy display reader.
    visible       BOOLEAN     NOT NULL DEFAULT TRUE,
    parent_id     INT         REFERENCES groups(id) ON DELETE SET NULL,
    -- Materialised chain depth, maintained by the trigger below. Root = 0.
    depth         SMALLINT    NOT NULL DEFAULT 0 CHECK (depth BETWEEN 0 AND 3),
    color         TEXT        NOT NULL DEFAULT 'secondary',
    title_color   TEXT        NOT NULL DEFAULT '',
    icon          TEXT        NOT NULL DEFAULT '',
    cost_points   INT         NOT NULL DEFAULT 0  CHECK (cost_points >= 0),
    duration_days INT         NOT NULL DEFAULT 30 CHECK (duration_days >= 1),
    sort_order    INT         NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT groups_no_self_parent CHECK (parent_id IS NULL OR parent_id <> id)
);

-- Cycle rejection and the depth limit are the SAME constraint: a cycle needs
-- some node to be its own strict ancestor, i.e. depth(n) > depth(n), which is
-- unsatisfiable. Maintaining child.depth = parent.depth + 1 therefore makes
-- cycles structurally impossible without a recursive CTE at write time.
--
-- The trigger alone is not sufficient: re-parenting A under its own descendant
-- reads a stale depth. The plugin does every parent_id write under
-- pg_advisory_xact_lock, walking the catalog to reject a descendant parent and
-- recomputing the subtree — see the store's SetParent.
-- The lookup is schema-qualified through TG_TABLE_SCHEMA rather than written
-- as a bare `groups`: a bare name resolves against the CALLER's search_path at
-- execution time, so the trigger would work inside the plugin's WithTx and
-- fail with "relation groups does not exist" for anyone else — an operator
-- running manual SQL mid-incident being the obvious case. This way it resolves
-- its own table no matter who writes, and survives a schema rename.
CREATE OR REPLACE FUNCTION groups_set_depth() RETURNS trigger AS $$
DECLARE pd SMALLINT;
BEGIN
    IF NEW.parent_id IS NULL THEN
        NEW.depth := 0;
    ELSE
        EXECUTE format('SELECT depth FROM %I.groups WHERE id = $1 FOR UPDATE', TG_TABLE_SCHEMA)
            INTO pd USING NEW.parent_id;
        IF pd IS NULL THEN
            RAISE EXCEPTION 'groups: parent % does not exist', NEW.parent_id;
        END IF;
        NEW.depth := pd + 1; -- CHECK (depth BETWEEN 0 AND 3) rejects 4+
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_groups_set_depth ON groups;
CREATE TRIGGER trg_groups_set_depth
    BEFORE INSERT OR UPDATE OF parent_id ON groups
    FOR EACH ROW EXECUTE FUNCTION groups_set_depth();

-- ── Per-group entitlement grants ─────────────────────────────────────────────
-- The keys a group confers. val is BIGINT to match core.EntitlementGrant.Val
-- (Go int) end to end, as user_entitlements does; 1 is the boolean convention.
CREATE TABLE IF NOT EXISTS group_entitlements (
    group_id INT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    key      TEXT   NOT NULL,
    val      BIGINT NOT NULL DEFAULT 1 CHECK (val >= 0),
    PRIMARY KEY (group_id, key)
);

-- ── Membership ───────────────────────────────────────────────────────────────
-- expires_at is NULLABLE (NULL = permanent), which the legacy NOT NULL column
-- could not express and which 'assigned' staff groups need. Every expiry sweep
-- MUST carry `expires_at IS NOT NULL` or it deletes permanent memberships on
-- its first tick.
--
-- No FK to users(id): that table lives in the host's schema and a plugin does
-- not own the host's referential integrity. Membership of a deleted user is
-- swept by the reconcile pass.
CREATE TABLE IF NOT EXISTS group_members (
    user_id    INT         NOT NULL,
    group_id   INT         NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    source     TEXT        NOT NULL DEFAULT 'purchase',
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_group_members_group ON group_members (group_id);
CREATE INDEX IF NOT EXISTS idx_group_members_expires
    ON group_members (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS group_member_history (
    id         BIGSERIAL   PRIMARY KEY,
    user_id    INT         NOT NULL,
    group_id   INT         REFERENCES groups(id) ON DELETE SET NULL,
    action     TEXT        NOT NULL,
    details    TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_group_member_history_user
    ON group_member_history (user_id, created_at DESC);

-- No seed. This migration creates the plugin's schema and nothing else.
--
-- It used to import the catalog and memberships from the host's
-- public.user_ranks trio, because ameNZB had five live tiers and two paying
-- members when the plugin was introduced. That was a fact about ONE database,
-- and baking it in made a one-off permanent and public: a plugin published to
-- loon-plugins runs on sites that have never heard of ameNZB, and a migration
-- reaching into a private host's schema is not portable however carefully it
-- is guarded.
--
-- Moving data from an existing system is a separate, deliberate, one-time
-- IMPORT that is allowed to know everything about its source — see
-- ADOPTION-MIGRATIONS.md. The SQL that used to live here is the starting point
-- for the host-side importer.
--
-- Production is unaffected: this file is recorded in core.plugin_migrations and
-- will never run there again, and the rows it seeded have been live since
-- 2026-07-26.

-- The sequence still needs initialising on a fresh catalog, or the first
-- CreateGroup collides with nothing and starts at an unhelpful value.
SELECT setval(pg_get_serial_sequence('groups', 'id'),
              GREATEST((SELECT COALESCE(MAX(id), 0) FROM groups), 1));
