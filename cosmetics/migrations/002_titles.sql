-- Custom titles: a member's own words under their name.
--
-- WHY THIS IS NOT JUST ANOTHER COSMETIC. Every other thing this plugin sells
-- comes from a fixed catalogue and cannot be made to say anything. A title is
-- TEXT SOMEBODY TYPED, published beside their name on every page they appear
-- on, which makes it the only part of this feature with a moderation surface —
-- and the reason there is a queue rather than a text box that publishes.

CREATE TABLE IF NOT EXISTS cosmetic_titles (
    -- One row per member, not one per submission. A rejected title is
    -- overwritten by the next attempt, because the history of what somebody
    -- tried to call themselves is not worth keeping and IS worth not keeping:
    -- a rejected title is usually rejected for being something nobody should
    -- have to read twice.
    user_id BIGINT PRIMARY KEY,

    text    TEXT NOT NULL,

    -- pending | approved | rejected. A title is published only when approved,
    -- which is the whole point of the queue.
    state   TEXT NOT NULL DEFAULT 'pending'
            CHECK (state IN ('pending', 'approved', 'rejected')),

    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Who looked at it and when, so a member asking "why was mine turned down"
    -- can be answered by the person who turned it down.
    reviewed_at TIMESTAMPTZ,
    reviewed_by BIGINT,
    -- The staff note, shown to the MEMBER. A rejection with no reason is one
    -- they will simply resubmit.
    reason  TEXT NOT NULL DEFAULT ''
);

-- The queue: everything waiting, oldest first. Partial because that IS the
-- query — staff never page through years of approved titles here.
CREATE INDEX IF NOT EXISTS cosmetic_titles_pending_idx
    ON cosmetic_titles (submitted_at)
    WHERE state = 'pending';

-- The renderer's query: every published title. Partial for the same reason.
CREATE INDEX IF NOT EXISTS cosmetic_titles_approved_idx
    ON cosmetic_titles (user_id)
    WHERE state = 'approved';
