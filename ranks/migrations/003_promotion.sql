-- Criteria for an EARNED rank, so the kind can finally mean something.
--
-- 'earned' has been in this table's CHECK, in the admin dropdown and in the Go
-- model since 001, and nothing ever evaluated it. The demo host seeds three
-- earned groups — Newcomer, Regular, Contributor — which no member could reach
-- by any path, because the only thing that ever added a membership was a
-- purchase or a staff hand.
--
-- All three default to 0, meaning "not a criterion". A group where all three
-- are zero is therefore NOT automatic, which is the important half: zero
-- thresholds read the other way would mean everyone qualifies, and the sweep
-- would promote the entire membership to whichever half-configured group an
-- operator had left lying around. See Group.Automatic.
ALTER TABLE groups ADD COLUMN IF NOT EXISTS min_uploaded BIGINT           NOT NULL DEFAULT 0 CHECK (min_uploaded >= 0);
-- Ratio as the site's accounting defines it, which is NOT upload/download for
-- a member who has downloaded nothing — see pluginapi.MemberStats. A ladder
-- gated on this alone promotes anyone who has uploaded a single byte and grabbed
-- nothing, which is why min_uploaded exists beside it.
ALTER TABLE groups ADD COLUMN IF NOT EXISTS min_ratio    DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (min_ratio >= 0);
ALTER TABLE groups ADD COLUMN IF NOT EXISTS min_age_days INT              NOT NULL DEFAULT 0 CHECK (min_age_days >= 0);
