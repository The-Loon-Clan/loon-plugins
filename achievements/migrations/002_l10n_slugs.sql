-- Localization slugs for the title and description (both optional).
--
-- When set, a host that carries a message catalogue resolves the slug in the
-- viewer's locale and the text columns become the fallback; empty means the
-- text columns ARE the content, which is every pre-002 row and every site
-- that never configures localization. The columns land ahead of the
-- catalogue UI being universal so definitions written today survive it.
ALTER TABLE achievements ADD COLUMN IF NOT EXISTS title_slug       TEXT NOT NULL DEFAULT '';
ALTER TABLE achievements ADD COLUMN IF NOT EXISTS description_slug TEXT NOT NULL DEFAULT '';
