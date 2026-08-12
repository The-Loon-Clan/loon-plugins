-- Staged articles are identified per GROUP, not globally.
--
-- 001_init declared `message_id TEXT PRIMARY KEY` and crawl.go stages with
-- ON CONFLICT (message_id) DO NOTHING. That reads as sound dedup — never stage
-- the same article twice — but it is scoped one level too wide, and the level
-- it skips is exactly the one that matters.
--
-- A crossposted article is ONE article filed under several newsgroups on the
-- server, carrying the SAME Message-ID in each (RFC 5536 s3.1.3: identity is the
-- Message-ID, and a simple octet comparison decides it). Crawling N groups
-- therefore yields that id N times, legitimately. But the staged SET is keyed
-- (group_name, base_subject) — see the articles_group_base index below and
-- groupArticles — so the first group to reach an article claims it globally and
-- every later group's copy is swallowed by the ON CONFLICT.
--
-- The consequence is silent and permanent. The second group's set is short by
-- exactly the articles the first group already staged, so it never satisfies
-- isComplete, never builds, and never salvages (pg mode runs no walk-past
-- sweep). It simply occupies rows until the prune horizon clears it, and
-- nothing logs a release that was never assembled.
--
-- Scoping the key to (group_name, message_id) preserves the dedup intent
-- exactly — the same article is still staged once per group, which is what the
-- ON CONFLICT was defending — while letting a crosspost populate every group's
-- set. It also matches the reference implementation: NNTmux keys parts
-- (binaries_id, partnumber), scoped to the collection, and normalises the
-- crosspost's group list separately rather than deduplicating the article away.
--
-- This affects the pg staging backend only. Production runs redis staging,
-- whose keys are already per-group (art:{group}:{hash}).
--
-- Idempotent: re-running finds the composite PK already in place and does
-- nothing. Dropping the old PK also drops the implicit unique index on
-- message_id alone; the lookups that matter go through articles_group_base.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'articles'
           AND c.contype = 'p'
           AND (SELECT count(*) FROM unnest(c.conkey)) = 1
    ) THEN
        -- Deduplicate before the composite key can be created. There is nothing
        -- to remove under the OLD key (it was unique on message_id alone, so a
        -- (group, message_id) pair cannot repeat), but doing this unconditionally
        -- keeps the migration safe if it is ever re-applied to a table that
        -- reached the composite shape by another route.
        DELETE FROM articles a
              USING articles b
              WHERE a.ctid < b.ctid
                AND a.group_name = b.group_name
                AND a.message_id = b.message_id;

        ALTER TABLE articles DROP CONSTRAINT articles_pkey;
        ALTER TABLE articles ADD PRIMARY KEY (group_name, message_id);
    END IF;
END $$;
