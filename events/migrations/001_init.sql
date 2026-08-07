-- events plugin schema. Applied by loon into the "events" schema with
-- search_path scoped, so unqualified names resolve here. Idempotent.
--
-- Lifted out of the rewards plugin, whose own comment said this was "not
-- reward-specific in meaning even though it lives here for now". Rewards gates
-- recurring payouts on a window; news wants to publish when an event opens;
-- neither should reach into the other. The move was done while every table was
-- EMPTY in production, which is the only reason it is a rename rather than a
-- migration of live payment references -- reward_grants.reference IS a window id
-- for recurring rewards.
--
-- No foreign keys out of this schema. A plugin that hard-links to another's
-- tables cannot be uninstalled, and consumers reference events by SLUG anyway.

CREATE TABLE IF NOT EXISTS events (
    id          BIGSERIAL PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',

    -- A generator, not a second source of truth: a job materialises windows
    -- ahead and the windows are what every query reads.
    cron        TEXT,

    -- How long a window stays open.
    --   set  -- the window CLOSES `duration` after opening. Gaps between.
    --   NULL -- the window runs until the next firing (contiguous), or, for a
    --          one-off with no next firing, never closes.
    -- Those last two are the same rule seen from two angles.
    duration    INTERVAL,

    -- When a ONE-OFF opens. NULL for a cron-driven event, whose starts come
    -- from the expression.
    --
    -- New here, and the reason the CHECK below is looser than the one this
    -- table replaces. Rewards had CHECK (duration IS NULL OR cron IS NOT NULL),
    -- so a duration REQUIRED a recurrence and "launch week: starts 1 Sep, runs
    -- 7 days" could not be written at all. It also generated nothing for a
    -- cron-less event, leaving one-off windows to be authored by hand.
    starts_at   TIMESTAMPTZ,

    -- "Midnight" is a timezone-relative claim, and a daily reset firing at UTC
    -- midnight is 8pm in New York. Absolute window rows need no timezone; the
    -- expression that generates them does.
    timezone    TEXT NOT NULL DEFAULT 'UTC',

    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- An event has to say WHEN, one way or the other. A row with neither a cron
    -- nor a start date can never open, and storing one is a definition an
    -- operator will later swear they configured.
    CONSTRAINT events_has_a_start CHECK (cron IS NOT NULL OR starts_at IS NOT NULL),
    -- A duration needs something to measure from, which either column supplies.
    CONSTRAINT events_duration_has_an_origin
        CHECK (duration IS NULL OR cron IS NOT NULL OR starts_at IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS event_windows (
    id        BIGSERIAL PRIMARY KEY,
    event_id  BIGINT      NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    starts_at TIMESTAMPTZ NOT NULL,

    -- A window that never closes is stored as a far-future end, NOT as NULL.
    -- Every "is it open" query is then one BETWEEN with no special case, and
    -- the CHECK below still means something. NULL would have made every
    -- consumer handle it and one of them would have forgotten.
    ends_at   TIMESTAMPTZ NOT NULL,

    -- Also the generator's idempotency: re-running it over a range it has
    -- already covered conflicts instead of duplicating.
    UNIQUE (event_id, starts_at),
    CHECK (ends_at > starts_at)
);

-- The only hot question: which window of event X contains now.
CREATE INDEX IF NOT EXISTS event_windows_open_idx
    ON event_windows (event_id, starts_at DESC, ends_at);
