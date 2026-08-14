-- What the BODY says a dropped posting is called.
--
-- Migration 037 recorded WHICH rule dropped a subject and WHICH article it was.
-- This is the answer half: the filename from the article's yEnc header, which
-- obfuscated posts routinely leave intact even when the subject is scrambled.
--
-- Nullable and observe-only, like 037. probed_at is what distinguishes "asked
-- and the body had no yEnc header" (recovered_name = '') from "not yet asked"
-- (probed_at IS NULL) -- without it an article with no header would be
-- re-fetched every pass forever, which is the expensive kind of mistake here
-- because every probe spends metered provider bytes.
ALTER TABLE subject_corpus ADD COLUMN IF NOT EXISTS recovered_name TEXT;
ALTER TABLE subject_corpus ADD COLUMN IF NOT EXISTS probed_at      TIMESTAMPTZ;

-- The probe's candidate query: dropped, has an article id, never asked.
-- Partial on all three, because the set it selects is a slice of a slice.
CREATE INDEX IF NOT EXISTS idx_subject_corpus_unprobed
    ON subject_corpus (seen_at DESC)
 WHERE junk_rule IS NOT NULL AND probed_at IS NULL;
