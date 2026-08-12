-- Content sketch: a dedup key that survives a partial view of one posting.
--
-- content_hash is sha256 over every observed message-id, so it answers "did we
-- see exactly these articles?". A crosspost is ONE posting carried in several
-- groups with identical message-ids, but each group is crawled separately and
-- retention, propagation lag and crawl timing mean each yields a slightly
-- different subset. Measured on the production index, one release was seen as
-- 79,081 articles in alt.binaries.teevee and 79,082 in alt.binaries.mom — 100%
-- of message-ids shared, one article apart, two different hashes, two rows.
--
-- content_sketch is sha256 over the 16 smallest message-id digests, so it
-- depends on the set rather than on how much of it we saw. See
-- contentSketchArticles in assemble.go for the tolerance argument.
--
-- The UNIQUE index is partial. Rows written before this migration have NULL and
-- must stay insertable; NULLs are not indexed, so no historical row can collide
-- and there is nothing to deduplicate first. The empty string is excluded for
-- the same reason: a release with no usable message-ids sketches to '' and must
-- not collide with every other such release.
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS content_sketch TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_nzbs_content_sketch
    ON nzbs (content_sketch)
    WHERE content_sketch IS NOT NULL AND content_sketch <> '';
