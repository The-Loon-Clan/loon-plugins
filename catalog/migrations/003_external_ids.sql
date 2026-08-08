-- Cross-ids for a catalog entry.
--
-- catalog_entry carries ONE (ext_namespace, ext_id): the id of the source that
-- produced the row. But sources hand back other databases' ids too — TVmaze
-- returns the IMDb and TheTVDB ids for every show it knows — and UpsertEntry
-- kept External[0] and dropped the rest, so those arrived and were discarded.
--
-- A side table rather than more columns on catalog_entry, because the set is
-- open-ended: a film can carry IMDb, TMDB, Wikidata and Letterboxd ids at once,
-- and a column per database means a migration per database.
--
-- PRIMARY KEY (entry_id, namespace) allows one id per namespace per entry and
-- makes the upsert idempotent — re-scraping a show rewrites its ids rather than
-- accumulating duplicates.
CREATE TABLE IF NOT EXISTS catalog_external (
    entry_id  BIGINT      NOT NULL REFERENCES catalog_entry(id) ON DELETE CASCADE,
    namespace TEXT        NOT NULL,
    value     TEXT        NOT NULL,
    PRIMARY KEY (entry_id, namespace)
);

-- The reverse lookup: "which entry is imdb tt0133093?", behind
-- catalog.CrossIDResolver. Without it that question is a sequential scan.
CREATE INDEX IF NOT EXISTS catalog_external_lookup
    ON catalog_external (namespace, value);
