-- Junk-title rules, so the filter is tunable without a code release.
--
-- Rows are seeded from the embedded seed/junk_rules.tsv on boot and then loaded
-- into memory (the check runs per article on the ingest hot path, so it must
-- never query this table). See junk.go.
--
-- source distinguishes shipped rules from operator-authored ones: re-seeding
-- updates only source='seed' rows, so local rules and local edits survive an
-- upgrade. `enabled` is never touched by a re-seed either, so disabling a
-- shipped rule sticks.
CREATE TABLE IF NOT EXISTS junk_rules (
    name       TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,                    -- regex | heuristic
    rule       TEXT NOT NULL DEFAULT '',         -- regex source, or heuristic id
    params     TEXT NOT NULL DEFAULT '{}',       -- JSON gates/tuning
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    source     TEXT NOT NULL DEFAULT 'seed',     -- seed | user
    notes      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
