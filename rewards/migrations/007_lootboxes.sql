-- Lootboxes: a named, weighted set of rewards, one of which is drawn when the
-- box is opened.
--
-- ONE table, and the box itself has no row of its own. A lootbox IS its slug —
-- the set of entries carrying it — which is the shape asked for and the honest
-- one: there is nothing to say about a box that its contents do not say, and a
-- header table would let a box exist with nothing in it, which is a box that
-- draws nothing and reports no error.
--
-- reward_id is a real foreign key, ON DELETE CASCADE. A reward that is deleted
-- takes its entries with it rather than leaving a box that draws a prize that
-- no longer exists; the alternative — a dangling id — fails at the moment a
-- member opens the box, which is the worst possible moment to find out.
--
-- weight is a positive integer, relative within the box: 50/30/20 and 5/3/2 are
-- the same box. Zero is refused rather than treated as "never": an entry that
-- can never be drawn is a mistake worth reporting, and disabling one is
-- deleting it.
CREATE TABLE IF NOT EXISTS lootbox_entries (
    id         BIGSERIAL PRIMARY KEY,
    -- The box this line belongs to. Slug rather than a name: it is referenced
    -- by payouts and store items, and renaming a display string must never
    -- break a reference (the rule ranks made explicit when it made slug the
    -- key).
    box_slug   TEXT   NOT NULL CHECK (box_slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    reward_id  BIGINT NOT NULL REFERENCES rewards(id) ON DELETE CASCADE,
    -- Relative chance within the box.
    weight     INT    NOT NULL DEFAULT 1 CHECK (weight > 0),
    -- Display order on the admin page and on whatever visual comes later. NOT
    -- the draw order — the draw is by weight — so two entries may share one.
    ordinal    INT    NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One line per reward per box. Two lines for the same prize is a weight,
    -- not a second entry, and allowing both makes the odds unreadable.
    UNIQUE (box_slug, reward_id)
);

-- The draw reads one box at a time; the admin page reads one box in order.
CREATE INDEX IF NOT EXISTS lootbox_entries_box_idx ON lootbox_entries (box_slug, ordinal, id);

-- A lootbox is also a thing a reward can PAY OUT: kind 'lootbox', target the
-- box slug. That makes every existing way of handing something over — an
-- achievement, a scheduled event, the pot's consolation, a store item — able
-- to hand over a box without any of them learning what a box is.
ALTER TABLE reward_payouts DROP CONSTRAINT IF EXISTS reward_payouts_kind_check;
ALTER TABLE reward_payouts ADD CONSTRAINT reward_payouts_kind_check
    CHECK (kind IN ('points','role','medal','achievement','username_fx','lootbox'));

-- Frozen grant lines carry the same vocabulary: a grant records what it paid,
-- and a box opened in the past must still describe itself.
ALTER TABLE reward_grant_payouts DROP CONSTRAINT IF EXISTS reward_grant_payouts_kind_check;
ALTER TABLE reward_grant_payouts ADD CONSTRAINT reward_grant_payouts_kind_check
    CHECK (kind IN ('points','role','medal','achievement','username_fx','lootbox'));
