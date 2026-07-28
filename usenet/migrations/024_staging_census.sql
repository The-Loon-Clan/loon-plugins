-- A time series of staging health, one row per build pass.
--
-- Diagnosing why completed releases stopped reaching the builder took most of a
-- day, and the reason it took that long is that every number involved was a
-- point-in-time reading with nothing to compare it against. "Staging pressure
-- 100%" says the ceiling is reached; it does not say whether Redis is EVICTING
-- to stay under that ceiling, and the plugin never read evicted_keys at all. A
-- set observed at 96% in one poll and absent in the next left no trace anywhere:
-- not in build_outcomes, not in the logs, not in any counter.
--
-- These columns are all cheap (INFO plus two O(1) reads) and all cumulative or
-- instantaneous, so the interesting quantity is the DELTA between consecutive
-- rows. Specifically:
--
--   evicted_keys rising          -> Redis is discarding keys to stay under
--                                   maxmemory; forming sets are being destroyed
--                                   before they can complete.
--   ready_depth >> sampled       -> the builder's random sample cannot keep up
--                                   with arrivals; entries wait past their TTL
--                                   and are dropped as fossils.
--   fossil_dropped rising        -> completed sets DID reach the queue and then
--                                   expired before the builder drew them.
--
-- Those three are the competing explanations for the same symptom, and until
-- now there was no way to tell them apart.
CREATE TABLE IF NOT EXISTS staging_census (
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The ready queue: sets that completed and are waiting to be assembled.
    ready_depth     BIGINT NOT NULL DEFAULT 0,  -- SCARD nzb:ready before the draw
    sampled         INTEGER NOT NULL DEFAULT 0, -- how many the pass drew
    live_candidates INTEGER NOT NULL DEFAULT 0, -- of those, still had their meta
    fossil_dropped  INTEGER NOT NULL DEFAULT 0, -- of those, meta was already gone

    -- Redis itself. maxmemory_policy is text because "which eviction policy" is
    -- the difference between "Redis silently deleted your release" and "Redis
    -- refused the write and the crawler would have errored".
    redis_keys      BIGINT NOT NULL DEFAULT 0,
    mem_used_bytes  BIGINT NOT NULL DEFAULT 0,
    mem_max_bytes   BIGINT NOT NULL DEFAULT 0,
    evicted_keys    BIGINT NOT NULL DEFAULT 0,
    expired_keys    BIGINT NOT NULL DEFAULT 0,
    maxmemory_policy TEXT  NOT NULL DEFAULT '',

    -- Sets mid-assembly, and how many of those the eviction rule WOULD discard
    -- if it were enabled. Observe-only: see staging_evict_stale_sets.
    pending_sets    INTEGER NOT NULL DEFAULT 0,
    hopeless_seen   INTEGER NOT NULL DEFAULT 0
);

-- Read newest-first, always.
CREATE INDEX IF NOT EXISTS idx_staging_census_at ON staging_census (at DESC);
