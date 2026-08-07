-- Migration 003 — the source catalogue becomes configuration.
--
-- 002 introduced achievements with a `metric`, and the catalogue that gave
-- those metrics names lived in Go: a host called StockSources(), edited the
-- list, and registered it. That is declaration, not configuration — adding a
-- countable thing meant a deploy, and an operator could not see the vocabulary
-- their own dropdowns were built from.
--
-- Everything else in this plugin is already a table an admin edits: rewards,
-- events, windows, payouts, achievements. This makes the catalogue the same,
-- so the only thing left in code is the part that genuinely cannot be data.
--
-- WHAT STAYS IN CODE, and why: the COUNTING. A MetricSource's SQL is bound to
-- a key here, and it is not stored. reward_issuances made the same call for
-- cohorts, in the same words -- "named cohorts, never stored SQL: an arbitrary
-- query in a table is an injection surface and unmaintainable". So the
-- vocabulary is config and the implementation is code, joined by the key.

CREATE TABLE IF NOT EXISTS reward_sources (
    -- The key IS the identity, and it is what rewards.trigger and
    -- achievements.metric store. Renaming one orphans everything pointing at
    -- it, which is why it is the primary key rather than a surrogate with a
    -- unique index: there is nothing to rename it to.
    key         TEXT PRIMARY KEY,
    label       TEXT NOT NULL,
    grp         TEXT NOT NULL DEFAULT '',

    -- Independent, because most things are both and some are only one: a post
    -- announces itself AND is counted for a lifetime, days-registered only
    -- counts, password-changed only fires.
    fires       BOOLEAN NOT NULL DEFAULT FALSE,
    counts      BOOLEAN NOT NULL DEFAULT FALSE,

    -- What is being counted, so achievements name themselves: "First post",
    -- "100 posts". Required when counts is set — an unnamed unit makes every
    -- suggestion blank, which is how a catalogue fills with achievement-3.
    unit        TEXT NOT NULL DEFAULT '',
    units       TEXT NOT NULL DEFAULT '',

    ordinal     INTEGER NOT NULL DEFAULT 0,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    -- Stock rows are seeded on first boot so the dropdowns are never empty at
    -- the moment they matter most. Flagged so an operator can tell what they
    -- were given from what they wrote, and so a later seed can leave edited
    -- rows alone.
    stock       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The same rule Valid() enforces in Go, as a constraint, so a row written
    -- by any other route cannot offer a dropdown entry that does nothing.
    CONSTRAINT reward_sources_usable CHECK (fires OR counts),
    CONSTRAINT reward_sources_named  CHECK (NOT counts OR unit <> '')
);

CREATE INDEX IF NOT EXISTS reward_sources_pickers_idx
    ON reward_sources (grp, label) WHERE enabled;
