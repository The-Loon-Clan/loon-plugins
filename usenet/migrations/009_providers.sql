-- Multiple providers: a pool of news servers rather than one.
--
-- Why this needs schema and not just config: NNTP article numbers are assigned
-- PER SERVER. The same article is number 4,812,003 on one backbone and something
-- entirely different on another. So watermarks and fetched-range coverage are
-- meaningful only in the context of the server that produced them, and the
-- single set of columns on `newsgroups` cannot describe two providers — whichever
-- crawled last would clobber the other, and the next pass would fetch ranges that
-- point at unrelated articles.
--
-- The payoff is more than capacity. Providers expire different articles, and
-- staging dedups on message-id (which IS global), so a release that is
-- incomplete on one backbone can be completed by another. Crawling N providers
-- into one staging area is therefore a completeness win, not just a throughput
-- one.

-- Roles: 'active' providers are crawled; 'backup' ones sit idle and are promoted
-- only when an active provider is failing. priority orders both (lower first).
ALTER TABLE servers ADD COLUMN IF NOT EXISTS name        TEXT    NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS role        TEXT    NOT NULL DEFAULT 'active';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS priority    INT     NOT NULL DEFAULT 100;
-- Per-provider connection cap; 0 means "use the plugin-wide default". Providers
-- sell different limits, so this cannot be one global number.
ALTER TABLE servers ADD COLUMN IF NOT EXISTS connections INT     NOT NULL DEFAULT 0;

-- Name defaults to the host so existing rows are identifiable in the UI.
UPDATE servers SET name = host WHERE name = '';

-- Per-(server, group) crawl state. This is the table that makes more than one
-- provider possible; the identically-named columns on `newsgroups` describe only
-- the first server and are left in place for one release so a rollback is safe.
CREATE TABLE IF NOT EXISTS newsgroup_state (
    server_id           INT     NOT NULL,
    group_name          TEXT    NOT NULL,
    high_watermark      BIGINT  NOT NULL DEFAULT 0,
    high_watermark_date TIMESTAMPTZ,
    back_watermark      BIGINT,
    back_watermark_date TIMESTAMPTZ,
    server_low          BIGINT  NOT NULL DEFAULT 0,
    server_high         BIGINT  NOT NULL DEFAULT 0,
    backfill_done       BOOLEAN NOT NULL DEFAULT FALSE,
    last_crawl          TIMESTAMPTZ,
    PRIMARY KEY (server_id, group_name)
);

-- Carry the existing single-server progress across, so upgrading does not
-- re-crawl. Attributed to the lowest server id — the only one that could have
-- produced those numbers.
INSERT INTO newsgroup_state (
    server_id, group_name, high_watermark, high_watermark_date,
    back_watermark, back_watermark_date, server_low, server_high,
    backfill_done, last_crawl)
SELECT (SELECT MIN(id) FROM servers), g.name, g.high_watermark, g.high_watermark_date,
       g.back_watermark, g.back_watermark_date, g.server_low, g.server_high,
       g.backfill_done, g.last_crawl
  FROM newsgroups g
 WHERE (SELECT MIN(id) FROM servers) IS NOT NULL
ON CONFLICT (server_id, group_name) DO NOTHING;

-- Coverage is per-server for the same reason. Existing rows belong to the first
-- server; the default of 0 would otherwise claim they apply to every provider,
-- which is exactly the "treat foreign ranges as covered and silently skip real
-- content" failure.
ALTER TABLE newsgroup_ranges ADD COLUMN IF NOT EXISTS server_id INT NOT NULL DEFAULT 0;
UPDATE newsgroup_ranges
   SET server_id = COALESCE((SELECT MIN(id) FROM servers), 0)
 WHERE server_id = 0;

CREATE INDEX IF NOT EXISTS idx_newsgroup_ranges_server
    ON newsgroup_ranges (server_id, group_name, range_start);
