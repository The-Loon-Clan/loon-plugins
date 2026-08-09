-- hitrun plugin schema. Applied by loon into the "hitrun" schema with
-- search_path scoped, so unqualified names resolve here.
--
-- This plugin READS tracker.user_stats and tracker.torrents and writes only its
-- own tables. The coupling is deliberate and one-way: hit-and-run is a policy
-- laid over the tracker's accounting, not part of it, and the tracker must stay
-- usable by a host that wants no punishment system at all.
--
-- Nothing here references users(id) or tracker.torrents(info_hash) with a
-- foreign key, for the reason the tracker's own migration gives: a plugin that
-- hard-links to tables it does not own cannot be uninstalled. Cleanup on member
-- deletion is a host-driven call.

-- ── Warnings: the record that a member failed to seed something ─────────────
--
-- UNIQUE on (user_id, info_hash) because a member is warned ONCE per torrent.
-- Without it a job that runs twice — or runs while a previous run is still
-- finishing — warns the same person repeatedly for the same offence, and the
-- count that disables their downloads is then a function of job scheduling
-- rather than of anything they did.
CREATE TABLE IF NOT EXISTS warnings (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL,
    info_hash  CHAR(40)    NOT NULL,
    -- Why it was issued, in the words shown to the member. Stored rather than
    -- derived so a rule change does not rewrite history: a warning issued under
    -- a 7-day rule should still say 7 days after the admin moves it to 3.
    reason     TEXT        NOT NULL DEFAULT '',
    issued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- When it stops counting. Set at issue time from the then-current expiry
    -- setting, for the same reason.
    expires_at TIMESTAMPTZ NOT NULL,
    -- Cleared early by a moderator, or by the member satisfying the requirement
    -- afterwards. Kept as a row rather than deleted so the history survives.
    cleared_at TIMESTAMPTZ NULL,
    UNIQUE (user_id, info_hash)
);
CREATE INDEX IF NOT EXISTS warnings_user_idx ON warnings (user_id);
-- The count that matters is ACTIVE warnings, which is the query the enforcement
-- decision makes on every evaluation.
CREATE INDEX IF NOT EXISTS warnings_active_idx
    ON warnings (user_id) WHERE cleared_at IS NULL;

-- ── Pre-warnings: the courtesy notice that precedes a warning ───────────────
--
-- Separate from warnings because it is not one. A member who reseeds after the
-- notice is never warned at all, and collapsing the two would make "how many
-- warnings do I have" depend on whether anyone had told them yet.
CREATE TABLE IF NOT EXISTS prewarnings (
    user_id   BIGINT      NOT NULL,
    info_hash CHAR(40)    NOT NULL,
    sent_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, info_hash)
);
