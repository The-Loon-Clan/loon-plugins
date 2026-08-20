-- Separate the words a member is PROPOSING from the words that are SHOWING.
--
-- The first cut kept one text column and set state back to 'pending' on every
-- submission, which is right about the queue and wrong about everything else:
-- editing an approved title deleted it from every page the moment you pressed
-- send, and left you with nothing under your name until a moderator got round
-- to you. A member changing one word was silently punished for it.
--
-- It also made the queue less useful than it should be. A moderator reading a
-- resubmission cannot tell "they asked for this" from "they changed something
-- already approved into this", and those are different judgements — but the
-- approved words had already been overwritten by the proposal, so there was
-- nothing left to compare against.
ALTER TABLE cosmetic_titles
    ADD COLUMN IF NOT EXISTS published TEXT NOT NULL DEFAULT '';

-- Anything already approved is, by definition, published.
UPDATE cosmetic_titles SET published = text WHERE state = 'approved' AND published = '';

-- The renderer now reads published, not text.
DROP INDEX IF EXISTS cosmetic_titles_approved_idx;
CREATE INDEX IF NOT EXISTS cosmetic_titles_published_idx
    ON cosmetic_titles (user_id)
    WHERE published <> '';
