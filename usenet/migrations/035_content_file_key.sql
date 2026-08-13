-- content_file_key on the plugin's own nzbs table (internal sink mode).
--
-- Mirrors the host's migration 301, which carries the full reasoning. In short:
-- content_sketch (migration 030) is a MinHash over message-ids, so it identifies
-- the same ARTICLES. That matches a crosspost, where one article is filed into
-- several groups and keeps one Message-ID (RFC 5536), and it can never match a
-- REPOST, where the same files are uploaded again and every article gets a new
-- id.
--
-- Measured on the production index: five rows displayed as five releases shared
-- 0 of ~79,082 message-ids and 731 of 731 filenames. Names are a property of the
-- content; ids and byte counts are properties of the posting and of what the
-- crawl happened to catch.
--
-- No unique index, unlike content_sketch. A sketch collision proves the same
-- articles; a filename collision only strongly implies the same content, and a
-- constraint that rejects an INSERT is the wrong place to meet the exception.
-- contentFileKeyDoc already declines to emit a key it cannot trust — any file
-- without a quoted name, or a lone file named too briefly to mean anything.
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS content_file_key TEXT;

CREATE INDEX IF NOT EXISTS idx_nzbs_content_file_key
    ON nzbs (content_file_key)
    WHERE content_file_key IS NOT NULL AND content_file_key <> '';
