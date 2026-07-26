-- Where staged releases actually go.
--
-- The build pass logged "built N from M candidates (skipped X blocked-ext, Y
-- blacklisted)" and nothing else, which left the two largest buckets invisible:
-- sets that were not complete yet, and sets the sink rejected as duplicates.
-- On a live site that showed up as 146 million staged articles producing 260
-- NZBs in twelve hours with no way to say which stage ate them — the counters
-- reported success and the shortfall had no name.
--
-- Bucketed by DAY rather than accumulated all-time (which is what filter_hits
-- does) because the question is almost always "what changed": an all-time
-- counter cannot distinguish a backlog that has always been there from one that
-- started this morning. One row per reason per day is a handful of rows a year.
--
-- Written once per build pass from an in-memory counter, never per release —
-- same discipline as filter_hits, for the same reason: the build loop drains up
-- to build_drain_per_pass sets and a write per set would cost more than the
-- assembly.
CREATE TABLE IF NOT EXISTS build_outcomes (
    day          DATE   NOT NULL,
    -- The outcome name. Go's buildOutcome consts are the authority; kept as
    -- TEXT so a new reason needs no migration.
    reason       TEXT   NOT NULL,
    total_count  BIGINT NOT NULL DEFAULT 0,
    -- One recent subject for this outcome. A count alone cannot tell you
    -- whether "junk" is catching junk or eating releases; the sample usually
    -- answers it at a glance. Same argument as filter_hits.last_sample.
    last_sample  TEXT   NOT NULL DEFAULT '',
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (day, reason)
);

-- The dashboard and any ad-hoc diagnosis both read newest-first.
CREATE INDEX IF NOT EXISTS idx_build_outcomes_day ON build_outcomes (day DESC);
