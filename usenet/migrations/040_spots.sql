-- Spotnet: the index itself.
--
-- A spot is one article that describes one release, which is the opposite of
-- what the crawler handles (many articles that must be reassembled into one
-- release). So spots get their own table and their own pass rather than a
-- branch through the article staging path — nothing in base_subject grouping,
-- segment counting or junk-by-subject applies to a row that arrives complete.
--
-- What they DO share is the watermark state below and the connection pool. A
-- spot group is an ordinary newsgroups row: high_watermark walks forward,
-- back_watermark walks backward, backfill_done ends it. That means the
-- Newsgroups tab, the coverage bar, the reset-watermark button and the
-- off-peak gate all work on free.pt with no new machinery.

ALTER TABLE newsgroups
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'articles';

-- 'articles' — a normal newsgroup the crawler assembles releases from.
-- 'spots'    — a Spotnet index group (free.pt). Read by the spot pass ONLY;
--              the crawler must skip it, because running the assembler over
--              5.9M one-article postings would stage millions of subjects that
--              can never complete a set.

-- The header half of a spot, straight off XOVER.
--
-- WHY THIS IS WORTH STORING ON ITS OWN, before any per-article fetch: XOVER
-- carries the poster, the signing key, the category, the size and the posted
-- time, at roughly one round trip per thousand articles. That is the whole of
-- free.pt's 5.9M-article history for a few thousand round trips. The document
-- (title, description, NZB pointer) needs a HEAD per spot and is millions of
-- fetches, so the cheap half runs to completion first and the expensive half
-- works off this table as its worklist.
CREATE TABLE IF NOT EXISTS spots (
    message_id    TEXT PRIMARY KEY,
    group_name    TEXT        NOT NULL,
    article_num   BIGINT      NOT NULL,

    -- From the packed From header (spot_header.go).
    poster        TEXT        NOT NULL DEFAULT '',
    subject       TEXT        NOT NULL DEFAULT '',
    public_key    TEXT        NOT NULL DEFAULT '',
    header_sig    TEXT        NOT NULL DEFAULT '',
    category      INT         NOT NULL DEFAULT 0,
    key_id        INT         NOT NULL DEFAULT 0,
    subcats       TEXT[]      NOT NULL DEFAULT '{}',
    -- BIGINT, and every comparison against it must cast ::bigint. Spot sizes
    -- run past 4GB routinely and int4 inference on a bound parameter has
    -- silently zeroed a worklist in this codebase before.
    size_bytes    BIGINT      NOT NULL DEFAULT 0,
    posted_at     TIMESTAMPTZ,
    locale        TEXT        NOT NULL DEFAULT '',

    -- Filled by the fetch pass (stage two). NULL fetched_at is the worklist.
    fetched_at    TIMESTAMPTZ,
    title         TEXT        NOT NULL DEFAULT '',
    description   TEXT        NOT NULL DEFAULT '',
    nzb_segment   TEXT        NOT NULL DEFAULT '',
    image_segment TEXT        NOT NULL DEFAULT '',
    -- '' until fetched, then one of verified / weak-key / unsigned. A spot
    -- whose signature was checked and FAILED is never stored at all.
    trust         TEXT        NOT NULL DEFAULT '',
    fetch_error   TEXT        NOT NULL DEFAULT '',

    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The stage-two worklist. Partial, because once the fetch pass has caught up
-- the overwhelming majority of rows are fetched and an index over all of them
-- would be most of the table and useless to the only query that runs.
CREATE INDEX IF NOT EXISTS spots_unfetched_idx
    ON spots (article_num DESC)
    WHERE fetched_at IS NULL;

-- Newest-first listing, and the join key for "what did we index today".
CREATE INDEX IF NOT EXISTS spots_posted_idx ON spots (posted_at DESC NULLS LAST);

-- Per-group progress without scanning: the spot pass reports coverage per
-- group and the Spots tab reads counts per group.
CREATE INDEX IF NOT EXISTS spots_group_article_idx ON spots (group_name, article_num);
