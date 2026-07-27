-- Finish what 020 started. 020's guard was wrong.
--
-- 020 skipped any group that already had ANY range row, on the reasoning that
-- an inherited claim must not overwrite coverage the crawler recorded for
-- itself. But an INSERT never overwrites — it adds a row — so the guard did
-- not protect anything. What it did do is skip exactly the groups that matter
-- most: one the plugin has been crawling since adoption has a range covering
-- the recent head, and nothing at all below it. On prod that left
-- alt.binaries.multimedia.anime.highspeed still reading 1.06% against a
-- watermark claiming full retention — the precise symptom 020 was written to
-- fix, on the one group that prompted it.
--
-- Fill only the span BELOW the lowest recorded range: from what the imported
-- watermarks claim up to just under where the crawler's own coverage starts.
-- Not the whole claim, so the two sources stay distinguishable and nothing is
-- double-counted; adjacent rather than overlapping, and recordFetchedRangeFor
-- absorbs adjacent runs (range_start <= end + 1) the next time a batch touches
-- the boundary, so the pair collapses to one fragment on its own.
--
-- The lower bound is floored at server_low: nothing below the server's oldest
-- article is fetchable, so claiming it would overstate coverage forever.
INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end)
SELECT s.backbone, s.group_name, claim.start_at, have.min_start - 1
  FROM newsgroup_state s
  JOIN LATERAL (
       SELECT GREATEST(COALESCE(s.back_watermark, s.server_low), s.server_low) AS start_at
  ) claim ON TRUE
  JOIN LATERAL (
       SELECT MIN(r.range_start) AS min_start
         FROM newsgroup_ranges r
        WHERE r.backbone = s.backbone AND r.group_name = s.group_name
  ) have ON TRUE
 WHERE have.min_start IS NOT NULL          -- 020 already handled the empty case
   AND claim.start_at < have.min_start     -- nothing to fill otherwise
   AND s.high_watermark > 0;
