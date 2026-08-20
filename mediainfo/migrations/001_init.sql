-- MediaInfo and screenshots: what somebody who actually downloaded a release
-- can tell everybody who has not.
--
-- WHY THIS IS CONTRIBUTED AND NOT DERIVED. An index holds pointers to Usenet
-- articles, not the bytes. The host already reports everything the NZB's own
-- file list proves (container, subtitle files, recovery share); bitrate, audio
-- tracks, muxed subtitle tracks and chapters are simply not in an NZB, and the
-- only honest way to have them is for a member who has the file to say so.
--
-- Which makes both tables USER-SUBMITTED CONTENT about somebody else's post,
-- and that shapes everything below: one row per release per member, an author
-- on every row, and staff able to remove.

CREATE TABLE IF NOT EXISTS reports (
    id         BIGSERIAL PRIMARY KEY,

    -- The release this describes. Not a foreign key: releases live in the
    -- usenet plugin's schema and age out with retention, and a hard reference
    -- would either block that cleanup or delete somebody's contribution with
    -- it. A row pointing at a gone release is simply never rendered.
    release_id BIGINT NOT NULL,
    user_id    BIGINT NOT NULL,

    -- The paste, verbatim. Kept alongside the parse because a parser that
    -- improves should be able to re-read what it was given, and because a
    -- member disputing what was rendered needs the original to point at.
    raw        TEXT NOT NULL,

    -- The parse. JSONB so the shape can grow without a migration for every
    -- field MediaInfo happens to emit — this is a report ABOUT a file, not a
    -- table of columns this plugin defines.
    parsed     JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at  TIMESTAMPTZ,
    -- Staff removal. Withheld rather than deleted, so a moderator can see what
    -- was removed and a repeat offender is visible as one.
    deleted_at TIMESTAMPTZ,
    deleted_by BIGINT,

    -- ONE REPORT PER MEMBER PER RELEASE. Two members describing the same
    -- release is useful — a re-encode and the original often differ, and
    -- disagreement is information. One member posting six is spam.
    UNIQUE (release_id, user_id)
);

-- The render query: this release's live reports, newest first.
CREATE INDEX IF NOT EXISTS reports_release_idx
    ON reports (release_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS shots (
    id         BIGSERIAL PRIMARY KEY,
    release_id BIGINT NOT NULL,
    user_id    BIGINT NOT NULL,

    -- Where the member said it lives. Kept for attribution and for a moderator
    -- checking a source, and NEVER rendered as an <img src> — see cache_path.
    source_url TEXT NOT NULL,

    -- Where this site put it after fetching it.
    --
    -- The whole point of the column. Hotlinking somebody's screenshot host
    -- sends every one of this site's readers to a third party on page load,
    -- which hands that host a log of who reads what — and lets it swap the
    -- image for anything it likes afterwards. So the file is fetched once,
    -- checked, and served from here.
    cache_path TEXT NOT NULL DEFAULT '',
    width      INT NOT NULL DEFAULT 0,
    height     INT NOT NULL DEFAULT 0,
    bytes      BIGINT NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    deleted_by BIGINT,

    -- The same image twice on one release is a mistake, not a contribution.
    UNIQUE (release_id, source_url)
);

CREATE INDEX IF NOT EXISTS shots_release_idx
    ON shots (release_id, created_at)
    WHERE deleted_at IS NULL AND cache_path <> '';
