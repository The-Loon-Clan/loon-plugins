-- Purchased transfer credit (pluginapi.TrackerCredit — the points store's
-- "GB Uploaded"/"GB Downloaded" items). Its OWN table rather than rows in
-- user_stats: that table is announce history keyed by (user, torrent) with a
-- torrents FK, and bought gigabytes did not happen on a torrent. Readers
-- fold this in when they sum; downloaded credit is FORGIVENESS, clamped at
-- read time so it can never dip a real transfer negative.
CREATE TABLE IF NOT EXISTS stat_credits (
    user_id    BIGINT PRIMARY KEY,
    uploaded   BIGINT NOT NULL DEFAULT 0,
    downloaded BIGINT NOT NULL DEFAULT 0
);
