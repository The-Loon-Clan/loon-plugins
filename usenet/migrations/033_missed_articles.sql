-- missed_articles: article numbers a successful OVER did not return.
--
-- THE PROBLEM. recordFetchedRangeFor marks the WHOLE requested range as covered
-- whenever a batch succeeded, without comparing what came back to what was
-- asked for. Walk-past eviction then reasons FROM that coverage: "the span is
-- fully covered and the set is still short" is taken as proof the missing
-- articles are never coming, and the release is judged dead or salvaged as
-- BROKEN. So an article the server merely omitted is indistinguishable, to
-- everything downstream, from one that does not exist.
--
-- WHY A TABLE AND NOT A RETRY. Most gaps are legitimate and permanent. RFC 3977
-- s6 makes article numbers sparse by design — removal, expiry and cancellation
-- leave holes — so OVER returning fewer lines than numbers requested is normal,
-- and blindly re-requesting every gap would re-walk expired history forever.
-- What distinguishes a real miss is that the number is RE-OFFERED on a later
-- attempt, which needs somewhere to remember the attempt.
--
-- attempts is therefore the whole point: a number seen missing once is
-- uninteresting, one still missing after several passes spread over hours is a
-- hole in retention, and one that resolves is exactly what this exists to
-- recover. give_up_at bounds the retry so a group whose history is genuinely
-- gone does not accumulate work forever.
--
-- Scoped per BACKBONE as well as per group: article numbers are per-(server,
-- group) coordinates (RFC 3977 s6), so the same number on two backbones is two
-- different articles and merging them would request nonsense.
CREATE TABLE IF NOT EXISTS missed_articles (
    backbone   TEXT        NOT NULL,
    group_name TEXT        NOT NULL,
    number     BIGINT      NOT NULL,
    attempts   INT         NOT NULL DEFAULT 1,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_try   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (backbone, group_name, number)
);

-- The retry planner asks "which numbers in this group are still worth another
-- look, oldest first" — attempts filters, last_try orders.
CREATE INDEX IF NOT EXISTS idx_missed_articles_retry
    ON missed_articles (backbone, group_name, attempts, last_try);
