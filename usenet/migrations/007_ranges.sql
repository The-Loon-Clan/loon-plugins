-- Fetched-range coverage, so backfill can work on real GAPS instead of a single
-- downward pointer.
--
-- Every successfully fetched+staged batch records its article-number span here,
-- and overlapping/adjacent spans are merged on write so the table stays small
-- (one row per contiguous run, not one per batch). The complement of these runs
-- between server_low and back_watermark is exactly the work backfill has left,
-- which is what lets gaps be fetched in PARALLEL rather than walking down one
-- batch at a time.
--
-- Article numbers are assigned per-server, so these rows are meaningful only for
-- the backbone that produced them: they are installation-local. Sharing them
-- between installations requires a backbone fingerprint (same numbering), or the
-- crawler would mark ranges covered that hold entirely different articles and
-- silently skip real content.
CREATE TABLE IF NOT EXISTS newsgroup_ranges (
    id          BIGSERIAL PRIMARY KEY,
    group_name  TEXT   NOT NULL,
    range_start BIGINT NOT NULL,
    range_end   BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_newsgroup_ranges_group
    ON newsgroup_ranges (group_name, range_start);
