-- tracker plugin schema. Applied by loon into the "tracker" schema with
-- search_path scoped, so unqualified names resolve here. Idempotent.
--
-- A faithful lift of the host's migration 147 (tracker_torrents +
-- tracker_user_stats), with the table names shortened because the schema now
-- qualifies them: tracker.torrents rather than public.tracker_torrents.
--
-- SAFE ONLY BECAUSE EVERYTHING IS EMPTY, verified against production before
-- writing this: 0 rows in tracker_torrents, 0 in tracker_user_stats, and 0 users
-- with tracker_access. Nothing to migrate, so this is a rename rather than a data
-- move.
--
-- NOT TO BE CONFUSED WITH THE OFFERS SYSTEM. public.private_trackers and
-- public.user_tracker_access both have "tracker" in the name and belong to the
-- offer system -- they record which EXTERNAL trackers a member has a verified
-- account on, keyed by tracker_id into private_trackers. They are untouched here,
-- and the resemblance is the easiest way to break the offers system by accident.

-- ── Torrents: what this tracker is tracking ─────────────────────────────────
--
-- No foreign key on uploaded_by. The host's migration had
-- `REFERENCES users(id) ON DELETE SET NULL`; users lives in a schema this plugin
-- does not own, and a plugin that hard-links to host tables cannot be
-- uninstalled. So the id is a plain BIGINT and cleanup on member deletion is a
-- host-driven call -- the same rule every other plugin here follows.
CREATE TABLE IF NOT EXISTS torrents (
    -- The 40-char hex info_hash, which IS the identity: a torrent is its info
    -- dict, and two uploads of the same content are the same torrent.
    info_hash    CHAR(40) PRIMARY KEY,
    name         TEXT   NOT NULL,
    size         BIGINT NOT NULL DEFAULT 0,
    piece_length BIGINT NOT NULL DEFAULT 0,
    file_count   INT    NOT NULL DEFAULT 1,
    files_json   JSONB  NULL,

    -- The info dict, stored byte-stably. Re-encoding it would change the hash,
    -- so the bytes are kept exactly as received and any tracker markers are
    -- stripped by virtue of storing ONLY the info dict rather than the whole
    -- torrent file.
    info_bytes   BYTEA  NOT NULL,

    uploaded_by  BIGINT NULL,
    -- The release this torrent belongs to, when it came from one. A plain id for
    -- the same reason as uploaded_by: nzbs is the host's table.
    nzb_id       BIGINT NULL,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Denormalised swarm counters, maintained by the announce path. Cheap to
    -- read on a listing; recomputable from user_stats if they ever drift.
    seeders      INT NOT NULL DEFAULT 0,
    leechers     INT NOT NULL DEFAULT 0,
    snatches     INT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS torrents_added_idx ON torrents (added_at DESC);
CREATE INDEX IF NOT EXISTS torrents_uploader_idx
    ON torrents (uploaded_by) WHERE uploaded_by IS NOT NULL;

-- ── Passkeys: how a torrent client identifies a member ──────────────────────
--
-- The credential baked into a .torrent's announce URL, so it is the tracker's
-- entire authentication story: an announce arrives with a passkey and nothing
-- else. Owned here rather than on users because it is meaningless outside the
-- tracker, and because there were zero of them to move.
--
-- UNIQUE and NOT the primary key on user_id alone, both deliberately: one member
-- has one passkey (rotation replaces it), and two members must never share one or
-- an announce cannot say who it is from.
CREATE TABLE IF NOT EXISTS passkeys (
    user_id    BIGINT PRIMARY KEY,
    passkey    TEXT   NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set on rotation, so a member can be told when their old .torrent files
    -- stopped working rather than discovering it as a silent announce failure.
    rotated_at TIMESTAMPTZ
);

-- ── Per-(member, torrent) byte counts ───────────────────────────────────────
--
-- uploaded/downloaded are monotonically increasing and last_seen is bumped on
-- every announce. The composite PK is what makes the announce handler's
-- INSERT ... ON CONFLICT a single round trip -- and an announce happens every
-- few minutes per peer, so that matters more here than anywhere else in the site.
--
-- Global per-member totals are a SUM over this table rather than a second
-- counter, so there is no second place to keep in sync. The host's original
-- comment said exactly that and it is worth keeping.
CREATE TABLE IF NOT EXISTS user_stats (
    user_id    BIGINT   NOT NULL,
    info_hash  CHAR(40) NOT NULL REFERENCES torrents(info_hash) ON DELETE CASCADE,
    uploaded   BIGINT   NOT NULL DEFAULT 0,
    downloaded BIGINT   NOT NULL DEFAULT 0,
    seedtime   BIGINT   NOT NULL DEFAULT 0,
    left_bytes BIGINT   NOT NULL DEFAULT 0,
    completed  BOOLEAN  NOT NULL DEFAULT FALSE,
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, info_hash)
);
CREATE INDEX IF NOT EXISTS user_stats_user_idx ON user_stats (user_id, last_seen DESC);
CREATE INDEX IF NOT EXISTS user_stats_torrent_idx ON user_stats (info_hash);

-- ── What is NOT here, and where it went ─────────────────────────────────────
--
-- users.passkey DOES come across -- see the passkeys table above. The first pass
-- of this file argued it should stay a host column because "it is minted at
-- registration". That was wrong, and checking settled it: 0 of 3,242 users have a
-- non-empty passkey, so registration mints nothing and the column has never held
-- a value. A passkey is meaningless outside the tracker, nothing else on the site
-- reads it, and there is no data to migrate -- so it belongs here and
-- public.users.passkey becomes dead weight the host can drop.
--
-- users.tracker_access does NOT come across, and does not become a column
-- here. Access is an ENTITLEMENT: the plugin asks
-- core.Entitlements.Has(ctx, userID, "tracker.access") and the host decides how
-- that is granted -- by role baseline, by a paid rank, by hand. That is the seam
-- core.Entitlements exists for, it is how the messages plugin already gates DMs,
-- and it means enabling the tracker for a cohort stops being an UPDATE on users.
--
-- Free to do now for the same reason the tables are: zero members currently hold
-- tracker_access, so there is no grant to backfill.
