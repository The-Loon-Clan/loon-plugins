-- Cheat detection: the sampling half.
--
-- user_stats holds CUMULATIVE totals and no history, so a rate can only be
-- measured by comparing the same counter at two points in time. These two
-- tables are that comparison and its output. The rules themselves are in
-- cheat.go and touch neither.

-- One row per (member, torrent): the LAST sample taken, never a log.
--
-- Deliberately not a history table. A per-announce log would be the better
-- detector and would also be a write on the hottest path the tracker has, plus
-- a table that grows without bound on an index this size. What is kept is the
-- single previous reading, which is all a rate needs, and it is overwritten
-- every sweep.
CREATE TABLE IF NOT EXISTS cheat_snapshots (
    user_id   BIGINT   NOT NULL,
    info_hash CHAR(40) NOT NULL REFERENCES torrents(info_hash) ON DELETE CASCADE,
    uploaded  BIGINT   NOT NULL,
    taken_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, info_hash)
);

-- What a sweep noticed, for a human to read.
--
-- Append-only, and nothing here acts on its own: every rule has a false
-- positive that is somebody's ordinary evening, and the cost of being wrong is
-- asymmetric. A missed cheat costs some ratio; a wrong ban costs a person.
--
-- cleared_at rather than DELETE, so "staff looked at this and it was fine" is
-- recorded rather than erased. The same flag firing again next sweep is
-- information — a member who was cleared once and keeps tripping the same rule
-- is a different conversation from a first-time reading.
CREATE TABLE IF NOT EXISTS cheat_flags (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT   NOT NULL,
    info_hash  CHAR(40) NOT NULL REFERENCES torrents(info_hash) ON DELETE CASCADE,
    kind       TEXT     NOT NULL,
    detail     TEXT     NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    cleared_at TIMESTAMPTZ NULL,
    cleared_by BIGINT   NULL
);

-- The staff queue reads open flags newest first; that is the only listing.
CREATE INDEX IF NOT EXISTS cheat_flags_open_idx
    ON cheat_flags (created_at DESC) WHERE cleared_at IS NULL;

-- One OPEN flag per (member, torrent, rule). Without it a sweep every fifteen
-- minutes turns one member's bad afternoon into ninety-six identical rows and
-- the queue becomes unreadable — which is the same as having no queue.
CREATE UNIQUE INDEX IF NOT EXISTS cheat_flags_unique_open
    ON cheat_flags (user_id, info_hash, kind) WHERE cleared_at IS NULL;
