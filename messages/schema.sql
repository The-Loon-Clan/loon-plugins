-- Schema the messages plugin expects the HOST to provide.
--
-- The plugin owns no tables: on the origin site the IRC plugin also writes
-- DMs for whisper delivery, so the schema is shared and host-owned. This file
-- exists so a host that does NOT already have these tables can create them in
-- one step, rather than reading the store to work out what they must look
-- like — the gap that made every earlier plugin port stall.
--
-- Every statement is idempotent. Adjust the users(id) references to whatever
-- the host's user table is called.

CREATE TABLE IF NOT EXISTS dm_threads (
    id              BIGSERIAL PRIMARY KEY,
    -- user_lo / user_hi enforce a canonical (LEAST, GREATEST) ordering
    -- so there's only one thread row per pair regardless of which
    -- side opened it first. Inserts MUST sort the two ids before
    -- writing (see storage helper EnsureDMThread).
    user_lo_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_hi_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Per-side soft delete. NULL = present in that user's inbox;
    -- non-NULL = hidden from that user's list. Re-receiving a
    -- message clears the corresponding delete (handler clamp).
    lo_deleted_at   TIMESTAMPTZ,
    hi_deleted_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (user_lo_id < user_hi_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_dm_threads_pair
    ON dm_threads (user_lo_id, user_hi_id);

-- "List my conversations" — index on each side, sorted by recency.
CREATE INDEX IF NOT EXISTS idx_dm_threads_lo_recent
    ON dm_threads (user_lo_id, last_message_at DESC)
    WHERE lo_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_dm_threads_hi_recent
    ON dm_threads (user_hi_id, last_message_at DESC)
    WHERE hi_deleted_at IS NULL;


CREATE TABLE IF NOT EXISTS dm_messages (
    id          BIGSERIAL PRIMARY KEY,
    thread_id   BIGINT  NOT NULL REFERENCES dm_threads(id) ON DELETE CASCADE,
    sender_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body        TEXT    NOT NULL,
    -- read_at is per-recipient. The sender's own row sets read_at
    -- to created_at at insert time (the sender has "read" their own
    -- message); the recipient's read_at stays NULL until they open
    -- the thread.
    read_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Thread fetch: every render of a thread does this exact scan.
CREATE INDEX IF NOT EXISTS idx_dm_messages_thread_chronological
    ON dm_messages (thread_id, created_at ASC);

-- Unread count: per-recipient (sender_id != viewer AND read_at IS NULL).
-- Partial index keeps the scan cheap as conversations grow.
CREATE INDEX IF NOT EXISTS idx_dm_messages_unread
    ON dm_messages (thread_id, sender_id)
    WHERE read_at IS NULL;


CREATE TABLE IF NOT EXISTS dm_blocks (
    blocker_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    blocked_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (blocker_id, blocked_id),
    CHECK (blocker_id <> blocked_id)
);

-- "Has user A blocked user B?" reverse-lookup for the send guard.
CREATE INDEX IF NOT EXISTS idx_dm_blocks_reverse
    ON dm_blocks (blocked_id, blocker_id);

-- ── Announcements (the broadcast half of the inbox) ──────────────────
CREATE TABLE IF NOT EXISTS messages (
    id         BIGSERIAL PRIMARY KEY,
    from_name  TEXT NOT NULL DEFAULT 'System',
    title      TEXT NOT NULL,
    body       TEXT NOT NULL,
    -- 'all' = broadcast, 'admin' = all admins, 'user:ID' = specific user
    target     TEXT NOT NULL DEFAULT 'all',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_target     ON messages (target);

CREATE TABLE IF NOT EXISTS message_reads (
    message_id BIGINT  NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    read_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, user_id)
);
