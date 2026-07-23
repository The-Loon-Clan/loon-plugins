-- Retire server_id from the crawl-state tables.
--
-- Migration 010 re-keyed crawl state on the BACKBONE (article numbers are
-- assigned per backbone, so two accounts on one backbone share numbering while
-- two backbones agree on nothing), carried every existing server_id across into
-- the backbone column, and swapped the primary key. What it did not do was
-- retire the column — which left newsgroup_state.server_id NOT NULL with no
-- default while every writer had stopped supplying it.
--
-- The effect on a fresh install was total: the first crawl pass tried to insert
-- its watermarks, hit "null value in column server_id violates not-null
-- constraint", and persisted nothing. The crawl itself appeared to work — it
-- fetched articles and staged them — but every pass restarted from zero.
--
-- The column is dropped rather than made nullable: backbone fully replaces it,
-- and a column that only ever holds NULL for new rows is a trap for the next
-- person who assumes it means something.
ALTER TABLE newsgroup_state  DROP COLUMN IF EXISTS server_id;
ALTER TABLE newsgroup_ranges DROP COLUMN IF EXISTS server_id;
