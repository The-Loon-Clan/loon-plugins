-- How an achievement LOOKS, chosen by the operator who defines it.
--
-- icon is a host sprite symbol name, the badge's fallback face; image_path is
-- an uploaded image's public URL, which wins when set. Both default to '',
-- meaning "the host decides" — which is exactly what every achievement got
-- before these columns existed, so old rows render unchanged.
ALTER TABLE achievements ADD COLUMN IF NOT EXISTS icon       TEXT NOT NULL DEFAULT '';
ALTER TABLE achievements ADD COLUMN IF NOT EXISTS image_path TEXT NOT NULL DEFAULT '';
