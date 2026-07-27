-- Backfill the coverage map for groups adopted BEFORE the seeding existed.
--
-- adoptHostState now records a fetched range matching the watermarks it
-- imports, but it runs exactly once and marks itself done in the settings
-- table. An install that adopted earlier has watermarks claiming full
-- retention and an EMPTY newsgroup_ranges, so the coverage strip renders
-- near-zero for a group whose history really is indexed. On prod that was
-- 1.05% shown against 218k releases held back to 2016.
--
-- This is the same statement adoption now runs, sourced from newsgroup_state
-- instead of the legacy host table — the backbone is already a column there,
-- so unlike the Go bootstrap this needs no runtime identity and can be a
-- migration.
--
-- Idempotent twice over: the NOT EXISTS skips any group that already has a
-- range recorded, so re-running adds nothing and a group the crawler has
-- since covered for itself is never overwritten with an inherited claim.
INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end)
SELECT s.backbone, s.group_name,
       GREATEST(COALESCE(s.back_watermark, s.server_low), s.server_low),
       s.high_watermark
  FROM newsgroup_state s
 WHERE s.high_watermark > 0
   AND s.high_watermark > GREATEST(COALESCE(s.back_watermark, s.server_low), s.server_low)
   AND NOT EXISTS (
       SELECT 1 FROM newsgroup_ranges r
        WHERE r.backbone = s.backbone AND r.group_name = s.group_name);
