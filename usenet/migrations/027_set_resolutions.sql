-- Completion-distance instrumentation (observe-only): one row per resolved
-- set — built, salvaged, or salvage-judged-dead — with its article span and
-- the group's watermarks at resolution time. The position-based staging
-- window is DERIVED from this data (window = p99.9 of how far behind the
-- walk head sets actually resolve, times a safety factor), never guessed:
-- the frontier-margin incident is the standing lesson on guessed destructive
-- thresholds. Pruned on the prune job's horizon.
CREATE TABLE IF NOT EXISTS set_resolutions (
    at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    group_name     TEXT        NOT NULL,
    kind           TEXT        NOT NULL, -- 'built' | 'salvaged' | 'salvage_dead'
    art_lo         BIGINT      NOT NULL,
    art_hi         BIGINT      NOT NULL,
    held           INT         NOT NULL,
    back_watermark BIGINT      NOT NULL,
    high_watermark BIGINT      NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_set_resolutions_at ON set_resolutions (at);
