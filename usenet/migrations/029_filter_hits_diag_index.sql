-- The Filters tab pages the instrument counters (ungrouped stems, merge
-- suspects, parse drops) ordered by hit count within a kind. Those rows are
-- unbounded — one day of the grouping watch produced 2,260 distinct stems —
-- so the page read needs an index rather than a sort over the whole table.
--
-- total_count DESC, rule matches the query's ORDER BY exactly, so a filtered
-- page is an index scan with no sort step.
CREATE INDEX IF NOT EXISTS filter_hits_kind_count_idx
    ON filter_hits (kind, total_count DESC, rule);

-- The prune deletes instrument rows by last_seen_at; without this it scans the
-- table on every prune pass to find a handful of expired stems.
CREATE INDEX IF NOT EXISTS filter_hits_last_seen_idx
    ON filter_hits (last_seen_at);
