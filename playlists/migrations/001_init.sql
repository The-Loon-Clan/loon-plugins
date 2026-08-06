-- playlists plugin schema — user-curated collections of indexed releases.
--
-- Applied by loon's plugin-migration runner (core.RunPluginMigrations) with
-- search_path scoped to the "playlists" schema, so the unqualified names below
-- become playlists.playlists / playlists.items. Append-only + idempotent, the
-- same rules as the host-numbered series.

CREATE TABLE IF NOT EXISTS playlists (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    -- slug is the public address (/playlists/<slug>) and is unique GLOBALLY,
    -- not per user: two owners cannot both hold "best-of-2026", because the
    -- URL has no room for the owner.
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    cover_url   TEXT NOT NULL DEFAULT '',
    -- Private by default. A collection is someone's working list until they
    -- say otherwise, and defaulting to public would publish it retroactively
    -- the moment this column arrived.
    public      BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_playlists_user ON playlists (user_id, updated_at DESC);
-- The index page lists public playlists newest-first; partial so it stays small
-- on a site where most collections are private.
CREATE INDEX IF NOT EXISTS idx_playlists_public ON playlists (updated_at DESC) WHERE public;

CREATE TABLE IF NOT EXISTS items (
    id          BIGSERIAL PRIMARY KEY,
    playlist_id BIGINT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
    -- release_id is NOT a foreign key on purpose. Releases live in the usenet
    -- plugin's own schema and age out with retention; a hard reference would
    -- either block that cleanup or cascade someone's collection away. A row
    -- pointing at a gone release is resolved to nothing at read time and shown
    -- as unavailable, which is the honest state.
    release_id  BIGINT NOT NULL,
    position    INTEGER NOT NULL DEFAULT 0,
    note        TEXT NOT NULL DEFAULT '',
    added_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One release appears at most once in a playlist; adding it twice is a no-op
-- rather than a duplicate row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_items_unique ON items (playlist_id, release_id);
CREATE INDEX IF NOT EXISTS idx_items_playlist ON items (playlist_id, position, added_at);
