-- Migration 004 — silent backfill.
--
-- Creating an achievement scored on a counter awards it retroactively to
-- everyone already past the threshold, on the next job tick, and notifies
-- every one of them. On 2026-08-07 that was 23 members within seconds of an
-- INSERT. Harmless at 23; a mailshot at 3,000.
--
-- The backfill itself is correct and is the point of an absolute counter. What
-- is wrong is only the NOTIFICATION: the badge should be awarded silently to
-- people who earned it before the achievement existed, and announced normally
-- to everyone after.

-- Marks a grant that should be handed over without telling anybody.
--
-- On reward_grants rather than on achievements, because it is not an
-- achievement concept. reward_issuances -- deliberate retroactive grants to a
-- named cohort -- wants exactly the same thing and predates this; an operator
-- back-paying six months of a reward should not fire six months of
-- notifications either. Payout handlers read it and skip the announcing part;
-- the paying part is unconditional.
ALTER TABLE reward_grants
    ADD COLUMN IF NOT EXISTS silent BOOLEAN NOT NULL DEFAULT FALSE;

-- When this achievement's first scoring pass finished.
--
-- NULL means it has never been scored, so the next pass is the backfill and
-- its completions are silent. Stamped once, after that pass, and every
-- completion afterwards notifies normally.
--
-- A timestamp rather than a boolean because "when did we backfill this" is the
-- question an operator asks when a member says they were never told, and a
-- boolean cannot answer it.
ALTER TABLE achievements
    ADD COLUMN IF NOT EXISTS backfilled_at TIMESTAMPTZ;
