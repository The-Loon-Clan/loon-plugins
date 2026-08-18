-- Where a release sits in a series, read from the title it already carries.
--
-- 66% of the titles in a real index say which episode of which show they are
-- (measured: 106,380 of 160,673, across 5,276 series). None of it was stored,
-- so "every copy of Silo S03E07" and "everything in season 3" were questions
-- the index held the answer to and could not be asked.
--
-- Columns on the release rather than a side table: this is a property OF the
-- release, one row each, read on every listing that groups by show. A join
-- table would be a second thing to keep in step for no gain.
--
-- Every column is nullable-by-default-empty rather than NULL: an unparsed
-- release has series_key = '' and season = 0, which reads the same in SQL and
-- in Go and needs no COALESCE at any call site. The partial index below is
-- what keeps the unparsed majority out of the way.
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS series_key  TEXT NOT NULL DEFAULT '';
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS series_name TEXT NOT NULL DEFAULT '';
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS season      INT  NOT NULL DEFAULT 0;
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS episode     INT  NOT NULL DEFAULT 0;
-- A whole-season release (S03.COMPLETE) rather than episode zero, which is a
-- real and different thing.
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS is_pack     BOOLEAN NOT NULL DEFAULT FALSE;

-- parsed_at is what makes the backfill restartable and re-runnable: NULL means
-- "this row has never been read", so the job can walk forward in batches and a
-- parser improvement can clear the column to re-read everything.
--
-- Distinct from series_key = '': a row parsed and found to be unfilable is
-- done, and must not be picked up on every pass forever.
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS episode_parsed_at TIMESTAMPTZ;

-- The series page's read: one show, its seasons, its episodes, newest first.
-- Partial, because two thirds of the table has no series and indexing '' would
-- be one enormous useless entry.
CREATE INDEX IF NOT EXISTS nzbs_series_idx
    ON nzbs (series_key, season, episode, id DESC)
    WHERE series_key <> '';

-- The backfill's own scan: the rows nobody has read yet.
CREATE INDEX IF NOT EXISTS nzbs_episode_unparsed_idx
    ON nzbs (id) WHERE episode_parsed_at IS NULL;
