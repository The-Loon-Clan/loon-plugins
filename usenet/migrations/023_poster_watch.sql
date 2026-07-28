-- Watch specific posters and record WHY their releases get dropped.
--
-- The gap this closes cost a full night of diagnosis. A poster the operator
-- knew was posting a hundred releases a day produced four in the catalogue, and
-- nothing anywhere said why: the articles were fetched, staged, and discarded by
-- three different mechanisms in turn, each of which counted its drops globally
-- but attributed none of them to a poster. "Is my crawler getting X's releases,
-- and if not, at which stage do they die" was unanswerable without reading the
-- code and inferring from aggregates.
--
-- filter_hits already answers "which rule is dropping the most", which is the
-- right question when tuning rules and the wrong one when chasing a specific
-- poster. This is that question asked the other way round.
CREATE TABLE IF NOT EXISTS poster_watch (
    -- Matched case-insensitively as a SUBSTRING of the article's From header,
    -- so "tsukihime" catches "TsukiHime <usenet.bot@tsukihime.org>" without the
    -- operator having to reproduce the exact formatting.
    pattern   TEXT PRIMARY KEY,
    enabled   BOOLEAN     NOT NULL DEFAULT TRUE,
    -- Free-text reminder of why this poster is being watched.
    note      TEXT        NOT NULL DEFAULT '',
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-poster outcome tally, deduped the same way filter_hits is: one row per
-- (poster, stage, reason) with a count and one recent sample, rather than a row
-- per event. A watched poster in a busy group produces tens of thousands of
-- articles an hour, and a log line each would cost more than the crawl.
CREATE TABLE IF NOT EXISTS poster_hits (
    poster        TEXT   NOT NULL,
    -- Where in the pipeline: 'ingest' (per-article, before staging) or
    -- 'build' (per-assembled-set).
    stage         TEXT   NOT NULL,
    -- Junk rule name, blacklist pattern, 'built', 'duplicate', 'incomplete',
    -- 'blocked-ext', ... — whatever the pipeline decided. Deliberately TEXT so
    -- a new outcome needs no migration.
    reason        TEXT   NOT NULL,
    total_count   BIGINT NOT NULL DEFAULT 0,
    last_sample   TEXT   NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (poster, stage, reason)
);

CREATE INDEX IF NOT EXISTS idx_poster_hits_last ON poster_hits (last_seen_at DESC);
