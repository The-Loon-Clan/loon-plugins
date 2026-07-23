-- Regex blacklist, lifted from the prod site (blacklist_regexes there).
--
-- Distinct from junk rules, and deliberately a separate table: junk rules are
-- SHIPPED defaults that detect machine-generated garbage nobody wants, and are
-- versioned with the plugin. Blacklist rules are the operator's own editorial
-- policy — a poster they don't want, a group they mirror but don't index, a
-- title pattern specific to their site. Mixing them would mean a plugin upgrade
-- could argue with an operator's decisions.
--
-- No seed data for the same reason: an empty blacklist is the correct default.
CREATE TABLE IF NOT EXISTS blacklist_regexes (
    id         BIGSERIAL PRIMARY KEY,
    pattern    TEXT    NOT NULL,
    -- Which part of the release the pattern is tested against:
    -- subject | title | poster | group. An unknown value never matches, so a
    -- typo fails closed (indexes everything) rather than open (drops everything).
    field      TEXT    NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Filter-hit counters for junk rules AND blacklist rules.
--
-- One table with a kind column rather than two: the question an operator asks is
-- the same either way — which rule is actually firing, how often, and on what —
-- and a single page answering it for both beats two pages answering it for one.
--
-- Counts accumulate across restarts. They are written in BATCHES at the end of a
-- pass, never per article: the filter runs on the ingest hot path, and a write
-- per dropped title would cost more than the crawl itself.
CREATE TABLE IF NOT EXISTS filter_hits (
    kind          TEXT   NOT NULL,   -- 'junk' | 'blacklist'
    rule          TEXT   NOT NULL,   -- junk rule name, or the blacklist pattern
    total_count   BIGINT NOT NULL DEFAULT 0,
    -- One recent title the rule dropped. This is what makes a rule reviewable:
    -- a count alone cannot tell you whether it is catching junk or eating
    -- releases, and the sample usually answers it at a glance.
    last_sample   TEXT   NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, rule)
);
