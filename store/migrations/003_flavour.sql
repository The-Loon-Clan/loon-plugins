-- Which site flavour an item belongs to: 'tracker', 'indexer' or 'both'.
-- The shop hides (and purchase refuses) items whose half of the site is off
-- — a GB-of-upload item on an indexer-only site would sell a number nothing
-- displays. The host says which halves are on through the
-- store.flavour seam; without it every item shows, which is every
-- pre-flavour host.
ALTER TABLE items ADD COLUMN IF NOT EXISTS flavour TEXT NOT NULL DEFAULT 'both';
