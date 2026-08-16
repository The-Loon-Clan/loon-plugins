-- Outcome counters: the denominators error_logs structurally cannot provide.
--
-- error_logs answers "what went wrong and how often", and it answers it well --
-- the host normalises digits and hex before hashing, so an article range or a
-- row id already collapses into one row. What it cannot answer is the question
-- that decides whether anything needs doing: how often did the same operation
-- SUCCEED.
--
-- 1,435 overview failures across 49 groups sounds alarming and is meaningless
-- on its own. Out of ten thousand attempts it is an outage; out of ten million
-- it is background noise on a public network, and the correct response is to
-- keep the retry and stop looking. Three weeks of that error being visible
-- without a denominator produced exactly no action, which is the cost.
--
-- So this counts BOTH, per day, per operation, per outcome. Successes are the
-- point; the failures are already logged elsewhere in more detail.
--
-- Day-grained rather than per-event: this is a trend, not an audit trail. One
-- row per (day, op, outcome) is a few hundred rows a month, needs no retention
-- job, and can be read with a plain GROUP BY. Per-event rows would be millions
-- a day to answer a question nobody asks at that resolution.

CREATE TABLE IF NOT EXISTS op_stats (
    day     DATE   NOT NULL DEFAULT CURRENT_DATE,
    op      TEXT   NOT NULL,
    -- 'ok', or a NORMALISED failure reason: the NNTP status code ('511',
    -- '430'), or a class for transport failures ('timeout', 'reset', 'pool').
    -- Normalised at the point of counting rather than stored raw, because an
    -- unbounded outcome is a cardinality explosion in a table whose whole
    -- purpose is to be cheap to aggregate.
    outcome TEXT   NOT NULL,
    count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, op, outcome)
);

-- Reading is always "recent days, all ops", so the day leads the primary key
-- and no second index is needed.
