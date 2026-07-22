-- Backbone identity.
--
-- Article NUMBERS are assigned per backbone, not per account: two providers that
-- resell the same backbone hand out identical numbers for identical articles,
-- while two different backbones agree on nothing numeric. (Message-ids are the
-- opposite — global, set by the poster — which is why staging dedup works across
-- providers even though watermarks cannot.)
--
-- So the right key for crawl state and coverage is the BACKBONE, not the server:
--
--   * Same backbone, two accounts -> one set of watermarks. The second account
--     is extra connections, not extra coverage; keying by server would make it
--     re-crawl every article the first already fetched.
--   * Different backbones -> separate state, and genuinely different content.
--
-- Default is "srv:<id>", i.e. every existing server is its own backbone. That is
-- the conservative assumption: treating two servers as one backbone when they
-- are not would make each skip ranges the other fetched, silently losing
-- articles. Operators opt in by naming the backbone in the wizard.
--
-- TODO: backbone names are typed by hand today. Public provider/backbone
-- listings exist and could populate a picker, and the same identity is what a
-- shared range-pack must match before it can be imported.
ALTER TABLE servers ADD COLUMN IF NOT EXISTS backbone TEXT NOT NULL DEFAULT '';

ALTER TABLE newsgroup_state  ADD COLUMN IF NOT EXISTS backbone TEXT NOT NULL DEFAULT '';
ALTER TABLE newsgroup_ranges ADD COLUMN IF NOT EXISTS backbone TEXT NOT NULL DEFAULT '';

-- Carry existing per-server state onto the default per-server backbone key, so
-- nothing is re-crawled on upgrade.
UPDATE newsgroup_state  SET backbone = 'srv:' || server_id WHERE backbone = '';
UPDATE newsgroup_ranges SET backbone = 'srv:' || server_id WHERE backbone = '';

-- One row per (backbone, group) now; the old (server_id, group) primary key
-- would keep two accounts on one backbone apart.
ALTER TABLE newsgroup_state DROP CONSTRAINT IF EXISTS newsgroup_state_pkey;
-- Collapse any duplicates the re-key created before re-adding the key: keep the
-- furthest-along row, since watermarks only ever move forward.
DELETE FROM newsgroup_state a
 USING newsgroup_state b
 WHERE a.backbone = b.backbone
   AND a.group_name = b.group_name
   AND (a.high_watermark, a.server_id) < (b.high_watermark, b.server_id);
ALTER TABLE newsgroup_state ADD PRIMARY KEY (backbone, group_name);

CREATE INDEX IF NOT EXISTS idx_newsgroup_ranges_backbone
    ON newsgroup_ranges (backbone, group_name, range_start);
