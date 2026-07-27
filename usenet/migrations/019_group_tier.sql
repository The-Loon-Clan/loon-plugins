-- Crawl priority becomes a named tier instead of a boolean.
--
-- low_priority could only express "after everyone else". It could not express
-- "before everyone else", which is the case that matters: one group is where
-- the content actually gets posted, and it was taking the same rotating slot
-- as twenty-seven others.
--
-- Three tiers, deliberately not an integer rank: the operator's real question
-- is "does this group come first, last, or with the crowd", and an integer
-- invites a spread of 10/20/25 that nobody can reason about later. Ordering
-- WITHIN a tier stays stalest-first, which is what stops the tail starving.
ALTER TABLE newsgroups
    ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'normal';

-- Enforced in the schema as well as in Go: the crawler branches on this value,
-- and a typo would silently sort a group into a tier that does not exist.
-- Idempotent because migrations re-run: ADD CONSTRAINT has no IF NOT EXISTS.
DO $$
BEGIN
    ALTER TABLE newsgroups
        ADD CONSTRAINT newsgroups_tier_check
        CHECK (tier IN ('critical', 'normal', 'low'));
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

-- Carry the boolean over. low_priority stays for now rather than being dropped
-- in the same migration: a rollback to the previous image must still find a
-- column it reads. It is written by nothing after this and can be dropped in a
-- later migration once the tier has shipped.
UPDATE newsgroups SET tier = 'low' WHERE low_priority = TRUE AND tier = 'normal';

-- Matches the selection order in activeGroupsForBackbone. Partial on active
-- because that is the only set the crawler ever reads.
CREATE INDEX IF NOT EXISTS idx_newsgroups_tier
    ON newsgroups (tier, sort_order, name) WHERE active = TRUE;
