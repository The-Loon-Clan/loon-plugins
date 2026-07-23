-- Per-group tuning.
--
-- Groups are not interchangeable: one may be enormous and worth throttling, one
-- may only be worth shallow history, and some should yield to the others when
-- connections are scarce. Until now every group got identical treatment, so the
-- only remedy for a problem group was to disable it entirely.
--
-- retention_days is NULL by default, meaning "use the plugin-wide crawl depth".
-- A NULL rather than a copied number matters: raising the global depth then
-- applies everywhere, instead of silently leaving every existing group pinned to
-- whatever the default happened to be when it was added.
ALTER TABLE newsgroups ADD COLUMN IF NOT EXISTS retention_days INT;

-- Milliseconds to pause after each batch for this group. Some providers rate
-- limit per group, and some groups are simply not worth saturating the pool for.
ALTER TABLE newsgroups ADD COLUMN IF NOT EXISTS throttle_ms INT NOT NULL DEFAULT 0;

-- Low-priority groups are crawled only after every normal group has had its
-- pass, so a huge low-value group cannot starve the ones that matter.
ALTER TABLE newsgroups ADD COLUMN IF NOT EXISTS low_priority BOOLEAN NOT NULL DEFAULT FALSE;

-- Manual ordering within a priority tier.
ALTER TABLE newsgroups ADD COLUMN IF NOT EXISTS sort_order INT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_newsgroups_order
    ON newsgroups (low_priority, sort_order, name) WHERE active = TRUE;
