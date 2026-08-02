-- Schema the communities plugin expects the HOST to provide.
--
-- The plugin owns no tables. On this site they arrived as host migrations
-- 252/253/255 in the public schema and stayed there through the extraction:
-- moving live tables into a plugin schema is a data migration, and the port
-- moved CODE. This file exists so a host that does NOT already have them can
-- create them in one step, rather than reading the store to work out what
-- they must look like.
--
-- Every statement is idempotent. Adjust the users(id) references to whatever
-- the host's user table is called.

CREATE TABLE IF NOT EXISTS communities (
    id              SERIAL      PRIMARY KEY,
    slug            TEXT        NOT NULL UNIQUE,
    name            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    sidebar_md      TEXT        NOT NULL DEFAULT '',
    banner_url      TEXT        NOT NULL DEFAULT '',
    icon_url        TEXT        NOT NULL DEFAULT '',
    accent_color    TEXT        NOT NULL DEFAULT '',
    owner_user_id   INTEGER     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Optional link to a release_groups row so a release-group owner
    -- can claim their group's community. NULL for generic / themed
    -- communities. Soft FK — group deletions don't cascade.
    release_group_id INTEGER    REFERENCES release_groups(id) ON DELETE SET NULL,
    nsfw            BOOLEAN     NOT NULL DEFAULT FALSE,
    -- Site-admin moderation. Hidden communities still resolve for
    -- the owner / admins (so they can fix the issue) but 404 for
    -- everyone else, same pattern as forum_threads.hidden_at.
    hidden_at       TIMESTAMPTZ,
    hidden_by       INTEGER     REFERENCES users(id) ON DELETE SET NULL,
    hidden_reason   TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_communities_owner ON communities(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_communities_release_group ON communities(release_group_id) WHERE release_group_id IS NOT NULL;

-- ── community_mods ────────────────────────────────────────────────
-- Extra moderators (the owner is implicit and not duplicated here).
-- ON DELETE CASCADE on both sides — losing the community or the
-- user removes the row. added_by is the user who promoted them
-- (owner or admin); useful for the mod-list audit display.
CREATE TABLE IF NOT EXISTS community_mods (
    community_id INTEGER     NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    user_id      INTEGER     NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    added_by     INTEGER     REFERENCES users(id)                ON DELETE SET NULL,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (community_id, user_id)
);

-- ── community_subscribers ─────────────────────────────────────────
-- "Join" button. Drives the community-card subscriber count and
-- the per-user feed of subscribed communities. Cheap many-to-many;
-- no role/notification preferences yet (Phase 2 if needed).
CREATE TABLE IF NOT EXISTS community_subscribers (
    community_id   INTEGER     NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    user_id        INTEGER     NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    subscribed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (community_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_community_subscribers_user ON community_subscribers(user_id);

-- ── community_rules ───────────────────────────────────────────────
-- Sidebar rule list. position controls render order (ascending).
-- Body is plain text; no markdown in this column to keep the rule
-- list scan-readable.
CREATE TABLE IF NOT EXISTS community_rules (
    id            SERIAL      PRIMARY KEY,
    community_id  INTEGER     NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    position      INTEGER     NOT NULL DEFAULT 0,
    title         TEXT        NOT NULL,
    body          TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_community_rules_community ON community_rules(community_id, position);

-- ── community_threads ─────────────────────────────────────────────
-- Threads inside a community. Mirrors forum_threads' shape (title,
-- pinned, locked, reply_count, last_post_at) plus community-mod
-- fields (removed_*) so a soft delete by a mod can carry a reason
-- distinct from the global hidden_at admin path. last_post_at is
-- bumped on every reply for ORDER BY recency.
CREATE TABLE IF NOT EXISTS community_threads (
    id              SERIAL      PRIMARY KEY,
    community_id    INTEGER     NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    user_id         INTEGER     NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    title           TEXT        NOT NULL,
    body            TEXT        NOT NULL DEFAULT '',
    pinned          BOOLEAN     NOT NULL DEFAULT FALSE,
    locked          BOOLEAN     NOT NULL DEFAULT FALSE,
    reply_count     INTEGER     NOT NULL DEFAULT 0,
    last_post_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Community-mod soft delete (separate from the admin hidden_at
    -- so we can show the right "removed by <mod>" badge).
    removed_at      TIMESTAMPTZ,
    removed_by      INTEGER     REFERENCES users(id) ON DELETE SET NULL,
    removed_reason  TEXT        NOT NULL DEFAULT '',
    -- Site-admin hide path (mirrors forum_threads.hidden_at).
    hidden_at       TIMESTAMPTZ,
    hidden_by       INTEGER     REFERENCES users(id) ON DELETE SET NULL,
    hidden_reason   TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_community_threads_community     ON community_threads(community_id, pinned DESC, last_post_at DESC);
CREATE INDEX IF NOT EXISTS idx_community_threads_user          ON community_threads(user_id);
CREATE INDEX IF NOT EXISTS idx_community_threads_last_post_at  ON community_threads(last_post_at DESC) WHERE hidden_at IS NULL AND removed_at IS NULL;

-- ── community_posts ───────────────────────────────────────────────
-- Replies. parent_post_id supports flat-with-quote replies for now;
-- nested threading can layer on later via the existing forum
-- quote-reply pattern.
CREATE TABLE IF NOT EXISTS community_posts (
    id              BIGSERIAL   PRIMARY KEY,
    thread_id       INTEGER     NOT NULL REFERENCES community_threads(id) ON DELETE CASCADE,
    user_id         INTEGER     NOT NULL REFERENCES users(id)              ON DELETE CASCADE,
    body            TEXT        NOT NULL,
    quoted_post_id  BIGINT      REFERENCES community_posts(id) ON DELETE SET NULL,
    -- Mod removal — same dual-path shape as threads.
    removed_at      TIMESTAMPTZ,
    removed_by      INTEGER     REFERENCES users(id) ON DELETE SET NULL,
    removed_reason  TEXT        NOT NULL DEFAULT '',
    hidden_at       TIMESTAMPTZ,
    hidden_by       INTEGER     REFERENCES users(id) ON DELETE SET NULL,
    hidden_reason   TEXT        NOT NULL DEFAULT '',
    edited_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_community_posts_thread     ON community_posts(thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_community_posts_user       ON community_posts(user_id);

CREATE TABLE IF NOT EXISTS community_join_requests (
    id               SERIAL      PRIMARY KEY,
    community_id     INTEGER     NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    user_id          INTEGER     NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    message          TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'pending',
    response_message TEXT        NOT NULL DEFAULT '',
    points_held      INTEGER     NOT NULL DEFAULT 0,
    decided_by       INTEGER     REFERENCES users(id) ON DELETE SET NULL,
    decided_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
DO $$ BEGIN
    ALTER TABLE community_join_requests
        ADD CONSTRAINT community_join_requests_status_chk
        CHECK (status IN ('pending', 'approved', 'denied', 'withdrawn'));
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;
-- At most one pending request per (community, user). A partial unique
-- index lets the same user re-request after a deny (the prior row is
-- 'denied', not 'pending') without tripping the constraint.
CREATE UNIQUE INDEX IF NOT EXISTS uq_community_join_requests_pending
    ON community_join_requests (community_id, user_id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_community_join_requests_queue
    ON community_join_requests (community_id, status, created_at);

-- ── community_invites ─────────────────────────────────────────────
-- Owner/mod-issued invite codes for invite_only (and optionally
-- gated) communities. A redeemed invite bypasses the requirement
-- gates. max_uses=0 means unlimited; use_count tracks redemptions.
-- expires_at NULL = no expiry.
CREATE TABLE IF NOT EXISTS community_invites (
    id           SERIAL      PRIMARY KEY,
    community_id INTEGER     NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    code         TEXT        NOT NULL UNIQUE,
    note         TEXT        NOT NULL DEFAULT '',
    created_by   INTEGER     REFERENCES users(id) ON DELETE SET NULL,
    max_uses     INTEGER     NOT NULL DEFAULT 0,
    use_count    INTEGER     NOT NULL DEFAULT 0,
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_community_invites_community ON community_invites(community_id);
