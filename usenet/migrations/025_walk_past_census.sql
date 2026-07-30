-- Walk-past eviction counter: the plugin's second deliberate shedding channel
-- (sets whose whole article span has been fetched yet are still incomplete —
-- the walk offered every article they could ever receive, so they can never
-- complete). Cumulative like hopeless_seen; readers take deltas.
ALTER TABLE staging_census ADD COLUMN IF NOT EXISTS walk_past bigint NOT NULL DEFAULT 0;
