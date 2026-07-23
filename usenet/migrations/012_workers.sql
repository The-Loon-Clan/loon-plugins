-- Worker presence, so newsgroups can be SPLIT between crawlers rather than
-- raced for.
--
-- Leases alone are not enough. A lease stops two workers crawling one group,
-- but with every worker greedily claiming whatever is free, the first to start
-- takes everything and the others find nothing left — alternation and
-- starvation, not parallelism. Splitting requires knowing how many crawlers
-- there are, which requires presence.
--
-- joined_at is what makes membership stable within a term. A worker that
-- appears mid-term is not counted until the next term boundary, so the divisor
-- (and therefore everyone's share) does not change underneath a pass already in
-- flight. Add a third crawler and it waits out the current term before taking
-- its third — which is the intended behaviour, not a limitation.
--
-- last_seen is the liveness signal: a worker that stops heartbeating drops out
-- of the divisor at the next term and its groups are redistributed.
CREATE TABLE IF NOT EXISTS crawler_workers (
    worker_id TEXT        PRIMARY KEY,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_crawler_workers_seen ON crawler_workers (last_seen);
