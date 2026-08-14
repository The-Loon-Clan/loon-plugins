-- What we dropped, and the article id needed to ask the body what it really is.
--
-- The crawler discards a junk-titled posting outright (crawl.go: "never index
-- it"). subject_corpus already samples those subjects, because differential
-- junk testing needs them — but it records neither WHICH RULE matched nor a
-- message-id, so the two questions anyone actually asks about a drop are both
-- unanswerable:
--
--   "what are we throwing away?"      -- needs the rule, to judge the rule
--   "was it really junk?"             -- needs the body, and so the article id
--
-- The second is the one that matters. A junk SUBJECT does not mean a junk
-- POSTING: usenet obfuscation commonly scrambles the subject and leaves the
-- yEnc header alone, so "541279675.bin" on the wire is frequently
-- "Some.Real.Release-GROUP.part03.rar" inside `=ybegin name=`. Without the
-- message-id we cannot check, and the scale makes that expensive to guess at:
-- 39,404 of the newest 50,000 releases had this shape when the junk rules were
-- last audited.
--
-- Both columns are nullable and observe-only. Nothing reads them yet; the
-- crawler's behaviour is unchanged by this migration. The point is to be able
-- to MEASURE the recovery rate before deciding whether to stop dropping, which
-- is the opposite order from how the last few of these went.
ALTER TABLE subject_corpus ADD COLUMN IF NOT EXISTS junk_rule  TEXT;
ALTER TABLE subject_corpus ADD COLUMN IF NOT EXISTS message_id TEXT;

-- The debug list's query: dropped rows, newest first. Partial, because junked
-- rows are a slice of a sampled table and the index should be the size of the
-- slice rather than the table.
CREATE INDEX IF NOT EXISTS idx_subject_corpus_junk
    ON subject_corpus (seen_at DESC)
 WHERE junk_rule IS NOT NULL;
