-- The per-file inventory: what exists on disk, how big, and its content hash.
--
-- This site has never had one. Nothing recorded a checksum, size or mtime for
-- any of the 417,336 asset files, which is why "is the backup complete" and
-- "did this file change" were both unanswerable, and why three different lists
-- of "directories that matter" could drift apart unnoticed.
--
-- Useful on its own, before any transfer protocol exists: it is the first thing
-- that can answer what is on disk, what changed, and what a restore would need.

-- A generation is one complete indexing pass.
--
-- The id comes from a SEQUENCE, never from a timestamp. This plugin has already
-- shipped one timezone bug in exactly this area (newestBackupAge's
-- ParseInLocation, a measured 9h-vs-2h skew), and a backup whose ordering
-- depends on wall-clock parsing is a backup that reorders itself when the clock
-- moves.
CREATE TABLE IF NOT EXISTS generations (
    id            BIGSERIAL PRIMARY KEY,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- NULL until the walk completes. An unsealed generation must never be
    -- served or counted: a partial walk looks exactly like a shrinking corpus,
    -- and treating it as authoritative is how retention deletes the only copy.
    sealed_at     TIMESTAMPTZ,
    files         BIGINT NOT NULL DEFAULT 0,
    bytes         BIGINT NOT NULL DEFAULT 0,
    -- Files whose content was hashed this run, as opposed to carried forward on
    -- an unchanged stat. The ratio is how much work a run actually did.
    hashed        BIGINT NOT NULL DEFAULT 0,
    -- Set when the walk failed part-way. A generation with an error is kept for
    -- diagnosis but is never sealed.
    error         TEXT NOT NULL DEFAULT ''
);

-- One row per (path, content). A file that changes gets a NEW row; the old row
-- stays, which is what makes the table a history rather than a snapshot.
--
-- Keyed on (path, sha256) rather than path alone because both identities
-- matter: path is what a RESTORE needs, content hash is what a TRANSFER needs,
-- and a file edited in place has one path with two contents.
CREATE TABLE IF NOT EXISTS files (
    id            BIGSERIAL PRIMARY KEY,
    -- Relative to the process working directory, e.g. web/static/covers/1.jpg.
    path          TEXT   NOT NULL,
    -- The registry slug (covers, screenshots, ...) so a policy can act per
    -- class without re-deriving it from the path on every query.
    class         TEXT   NOT NULL,
    sha256        TEXT   NOT NULL,
    size_bytes    BIGINT NOT NULL,

    -- The stat gate. size+mtime alone is not enough: if the clock steps
    -- backward (NTP correction, VM migration) and a file is rewritten to the
    -- same size, mtime can repeat and the change is invisible. ctime cannot be
    -- set backward from userspace, and inode catches a replace-by-rename that
    -- preserved both.
    mtime_ns      BIGINT NOT NULL,
    ctime_ns      BIGINT NOT NULL DEFAULT 0,
    inode         BIGINT NOT NULL DEFAULT 0,

    first_gen     BIGINT NOT NULL REFERENCES generations(id),
    last_gen      BIGINT NOT NULL REFERENCES generations(id),
    -- When this content was last actually read and hashed, as opposed to
    -- carried forward. Drives the rolling re-hash that catches bit-rot and
    -- torn writes the stat gate cannot see.
    hashed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (path, sha256)
);

-- "What is the current content at this path" — the common query, and the one a
-- restore walks.
CREATE INDEX IF NOT EXISTS idx_files_path_gen ON files (path, last_gen DESC);
-- "Is this content already known" — the transfer-side identity.
CREATE INDEX IF NOT EXISTS idx_files_sha ON files (sha256);
-- Per-class counts and byte totals, which the shrink gate compares between
-- generations to refuse a class that has collapsed.
CREATE INDEX IF NOT EXISTS idx_files_class_gen ON files (class, last_gen);
-- The rolling re-hash picks the oldest-verified rows.
CREATE INDEX IF NOT EXISTS idx_files_hashed_at ON files (hashed_at);

-- Per-class totals per generation, so the shrink gate is a lookup rather than
-- an aggregate over millions of rows.
CREATE TABLE IF NOT EXISTS class_stats (
    gen        BIGINT NOT NULL REFERENCES generations(id) ON DELETE CASCADE,
    class      TEXT   NOT NULL,
    files      BIGINT NOT NULL DEFAULT 0,
    bytes      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (gen, class)
);

-- Files that could not be indexed: unreadable, or structurally truncated.
--
-- Recorded rather than skipped silently. A file the backup cannot read is
-- exactly the file most likely to be the one you need, and a walk that quietly
-- omits it produces a manifest that is complete by its own accounting and
-- missing the thing that mattered.
CREATE TABLE IF NOT EXISTS suspect (
    path       TEXT PRIMARY KEY,
    class      TEXT NOT NULL,
    reason     TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    seen_count BIGINT NOT NULL DEFAULT 1
);
