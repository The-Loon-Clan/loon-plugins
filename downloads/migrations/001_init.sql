-- What a member's download client said about a release.
--
-- ONE ROW PER MEMBER PER RELEASE, not one per report. A post-processing script
-- runs every time a job finishes, and a member who retries a failing download
-- four times has said one thing four times, not four things. Upserting keeps
-- the table proportional to "members × releases they had an opinion about"
-- rather than to how many times their client retried — and it makes the read
-- that matters ("how many DISTINCT members hit this") a plain count instead of
-- a count of distinct user_ids over an ever-growing log.
--
-- The cost is that the history of one member's opinion changing is not kept.
-- That is the right trade: the useful question is what they think NOW (a retry
-- that succeeded after a failure means the release is fine), and reports is
-- there for the operator who wants to know it took four attempts.
CREATE TABLE IF NOT EXISTS download_reports (
    user_id    BIGINT NOT NULL,
    release_id BIGINT NOT NULL,

    -- 'ok' | 'failed'. Deliberately two values and not the client's own
    -- vocabulary: SABnzbd distinguishes failed-verification from failed-unpack
    -- and NZBGet has its own set, but the site can act on exactly one
    -- distinction — did this work — and storing a richer status nothing reads
    -- would be a column that drifts out of date with two other projects.
    -- The client's own wording is kept in detail, where it is evidence rather
    -- than a value anything branches on.
    status     TEXT NOT NULL CHECK (status IN ('ok', 'failed')),
    detail     TEXT NOT NULL DEFAULT '',
    client     TEXT NOT NULL DEFAULT '',

    -- How many times this member's client has said it. A retry loop shows up
    -- here rather than as rows.
    reports    INT NOT NULL DEFAULT 1,
    first_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (user_id, release_id)
);

-- The read every consumer wants: "how does this release look to the people who
-- actually downloaded it". Partial on failures, because 'ok' rows are the
-- overwhelming majority on a healthy index and no page asks to list them.
CREATE INDEX IF NOT EXISTS download_reports_failed_idx
    ON download_reports (release_id, last_at DESC)
    WHERE status = 'failed';

-- The staff view's ordering: newest opinions first, whatever they say.
CREATE INDEX IF NOT EXISTS download_reports_recent_idx
    ON download_reports (last_at DESC);
