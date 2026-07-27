-- Undo the inherited coverage seeded by migrations 020 and 021.
--
-- Those recorded, as FETCHED, the span the legacy crawler's watermarks claimed
-- it had indexed. The reasoning was that a group holding 218k releases back to
-- 2016 should not render a 1% coverage bar. Half right, and wrong where it
-- counts: production's monthly figures show the old crawler's coverage of that
-- span degrading from roughly 2025-09 — distinct posters seen per month fell
-- from 57 to single digits and stayed there for ten months. The inherited claim
-- asserts coverage that demonstrably is not there.
--
-- It is not a cosmetic mistake. The BACKFILL plans from gaps in this table
-- (backfillGapsFor -> gapJobs), and marks a group complete when it finds none.
-- A seeded range spanning server_low to the crawler's own start leaves the
-- group contiguously covered, so a backfill reset finds zero gaps and instantly
-- re-marks itself done. The seeding closed the only door to re-reading exactly
-- the span where the releases went missing.
--
-- Deleted by reproducing the insert signature: both migrations computed
-- range_start as GREATEST(COALESCE(back_watermark, server_low), server_low),
-- which a range recorded by an actual crawl has no reason to match. If one
-- coincidentally does, the cost is re-reading a span we already hold, and
-- content_hash dedup makes that cheap — the asymmetry is deliberate, because
-- under-deleting leaves the recovery blocked and silently so.
DELETE FROM newsgroup_ranges r
 USING newsgroup_state s
 WHERE s.backbone = r.backbone
   AND s.group_name = r.group_name
   AND r.range_start = GREATEST(COALESCE(s.back_watermark, s.server_low), s.server_low);
