-- duration becomes a plain integer count of seconds.
--
-- INTERVAL came across with the lift from rewards, and it was never paying for
-- itself. lib/pq cannot scan INTERVAL into a time.Duration, so every read went
-- through EXTRACT(EPOCH FROM duration) and every write built a "%d seconds"
-- string for Postgres to re-parse -- a round trip through two grammars to move
-- one number. An INTEGER goes straight in and straight out.
--
-- It also removes a class of surprise the INTERVAL type invites: '1 month' is a
-- legal interval and is NOT a fixed length of time, so an operator could store a
-- duration whose meaning depended on which month the window opened in, and the
-- generator's start.Add(duration) would silently disagree with what Postgres
-- would have computed. Seconds cannot express that, which is the point.
--
-- Additive then destructive, in that order, so the backfill cannot lose data on
-- a host where these rows are not empty. On THIS host both tables are empty --
-- which is why the whole extraction is happening now -- but a migration that
-- only works on an empty table is a trap for whoever restores an older dump.

ALTER TABLE events ADD COLUMN IF NOT EXISTS duration_seconds INTEGER;

-- Carry any existing INTERVAL over before the column goes. EXTRACT(EPOCH) on a
-- month-based interval resolves it to a nominal 30-day month, which is the best
-- available answer and the reason the column type is changing.
UPDATE events
   SET duration_seconds = EXTRACT(EPOCH FROM duration)::INTEGER
 WHERE duration IS NOT NULL AND duration_seconds IS NULL;

ALTER TABLE events DROP COLUMN IF EXISTS duration;

-- NULL still means "no duration" -- contiguous for a recurring event, never
-- closing for a one-off. Zero would be a window of no length, which is not the
-- same thing and is why this stays nullable rather than defaulting to 0.
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_duration_has_an_origin;
ALTER TABLE events ADD CONSTRAINT events_duration_has_an_origin
    CHECK (duration_seconds IS NULL OR cron IS NOT NULL OR starts_at IS NOT NULL);

-- A duration of zero or less is a definition that can never be open. Rejecting
-- it here beats discovering it as a window whose ends_at fails the existing
-- ends_at > starts_at check, which would surface as a generator log line rather
-- than as a form error.
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_duration_positive;
ALTER TABLE events ADD CONSTRAINT events_duration_positive
    CHECK (duration_seconds IS NULL OR duration_seconds > 0);
