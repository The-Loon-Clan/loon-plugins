-- Cosmetics: what a member has unlocked, and what they are wearing.
--
-- TWO TABLES, because owning and wearing are different facts and a site that
-- conflates them has no answer to "I bought three, let me switch". One row per
-- unlock, one row per member saying which unlock is live.

CREATE TABLE IF NOT EXISTS cosmetic_owned (
    user_id BIGINT NOT NULL,

    -- The catalogue slug (pluginapi.Effects). Deliberately NOT a foreign key
    -- to a table of effects: the catalogue is code in both repositories,
    -- because half of an effect is CSS, and a second copy in the database
    -- would be a third place to disagree.
    slug    TEXT NOT NULL,

    -- Where it came from, for a member reading their own list and for staff
    -- reading somebody's: 'store' or 'grant'.
    source  TEXT NOT NULL DEFAULT 'store',

    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- NULL means it is theirs for good. A dated unlock is what makes a VIP
    -- cosmetic possible: it lapses, the name goes back to normal, and nothing
    -- has to remember to take it away.
    expires_at TIMESTAMPTZ,

    PRIMARY KEY (user_id, slug)
);

-- The renderer's query: every member wearing something, right now. Partial on
-- the live rows because that IS the query — an index over expired unlocks
-- would be pages of rows nothing ever reads.
CREATE INDEX IF NOT EXISTS cosmetic_owned_live_idx
    ON cosmetic_owned (user_id)
    WHERE expires_at IS NULL OR expires_at > NOW();

CREATE TABLE IF NOT EXISTS cosmetic_equipped (
    user_id BIGINT NOT NULL,

    -- The SLOT this effect occupies. One row per member per slot, so equipping
    -- is an upsert and there is no way to end up wearing two.
    --
    -- Only 'name' exists today: it is the one place a username is drawn with
    -- any styling at all. The column is here rather than a bare user_id
    -- primary key because the second slot — a custom title, a badge — is a
    -- value in this column and no migration, and because a table that assumed
    -- one slot would have to be rewritten to gain another.
    slot    TEXT NOT NULL,

    slug    TEXT NOT NULL,
    equipped_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (user_id, slot)
);
