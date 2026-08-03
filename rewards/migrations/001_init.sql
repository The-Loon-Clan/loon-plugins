-- rewards plugin schema. Applied by loon into the "rewards" schema with
-- search_path scoped, so unqualified names resolve here. Idempotent.
--
-- No foreign keys to the host's users table: it lives in another schema this
-- plugin does not own, and a plugin that hard-links to host tables cannot be
-- uninstalled. User ids are therefore plain BIGINTs, and cleanup on member
-- deletion is a host-driven call rather than a cascade.

-- ── Events: just event data ─────────────────────────────────────────────────
-- A season, a launch week, or the plain daily tick. Not reward-specific in
-- meaning even though it lives here for now.
CREATE TABLE IF NOT EXISTS events (
    id          BIGSERIAL PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    -- A generator, not a second source of truth: a job materialises windows
    -- ahead and the windows are what every query reads. NULL = one-off,
    -- windows authored by hand on the calendar.
    cron        TEXT,
    -- The whole season/reset distinction, in one nullable column:
    --   set  — the window CLOSES `duration` after opening. Gaps between.
    --   NULL — the window runs until the next firing. Contiguous, forever.
    duration    INTERVAL,
    -- "Midnight" is a timezone-relative claim, and a daily reset firing at UTC
    -- midnight is 8pm in New York. Absolute window rows need no timezone; the
    -- expression that generates them does.
    timezone    TEXT NOT NULL DEFAULT 'UTC',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A duration with no recurrence has no "starting when".
    CHECK (duration IS NULL OR cron IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS event_windows (
    id        BIGSERIAL PRIMARY KEY,
    event_id  BIGINT      NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at   TIMESTAMPTZ NOT NULL,
    -- Also the generator's idempotency: re-running it over a range it has
    -- already covered conflicts instead of duplicating.
    UNIQUE (event_id, starts_at),
    CHECK (ends_at > starts_at)
);
-- The only hot question: which window of event X contains now.
CREATE INDEX IF NOT EXISTS event_windows_open_idx
    ON event_windows (event_id, starts_at DESC, ends_at);

-- ── Rewards: what is earnable, and on what terms ────────────────────────────
CREATE TABLE IF NOT EXISTS rewards (
    id       BIGSERIAL PRIMARY KEY,
    slug     TEXT NOT NULL UNIQUE,
    name     TEXT NOT NULL DEFAULT '',
    -- one_off   — at most once ever.         reference = 0
    -- recurring — at most once per window.   reference = event_windows.id
    -- per_unit  — the delta since last paid. reference = high-water mark
    kind     TEXT NOT NULL CHECK (kind IN ('one_off','recurring','per_unit')),
    -- Gates when the reward is earnable AND, for kind='recurring', IS the
    -- reset: the next window is the next entitlement. NULL = always earnable.
    -- RESTRICT because deleting an event out from under paid grants would
    -- orphan the reference they are keyed on.
    event_id BIGINT REFERENCES events(id) ON DELETE RESTRICT,
    -- Which surface offers this: the login page asks for 'login', the upload
    -- path for 'upload', and neither knows what the other shows.
    trigger  TEXT NOT NULL DEFAULT '',
    -- NULL = a claim never expires, the default on purpose: a reward that
    -- evaporates because someone was on holiday becomes a support ticket.
    expires_after INTERVAL,
    delivery TEXT NOT NULL DEFAULT 'auto' CHECK (delivery IN ('auto','claim')),
    enabled  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (kind <> 'recurring' OR event_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS rewards_trigger_idx ON rewards (trigger) WHERE enabled;

-- ── Payouts: what a reward hands over ───────────────────────────────────────
-- A reward is 1..N of these. "100 points and the Founder medal" is two rows,
-- not two rewards -- which is why there is no parent/child mechanism: nesting
-- rewards bought a cycle risk and a reference collision for nothing.
CREATE TABLE IF NOT EXISTS reward_payouts (
    id        BIGSERIAL PRIMARY KEY,
    reward_id BIGINT NOT NULL REFERENCES rewards(id) ON DELETE CASCADE,
    kind      TEXT NOT NULL CHECK (kind IN ('points','role','medal','achievement','username_fx')),
    -- WHICH one, for every kind that names something rather than counting it.
    -- 'points' is the only kind whose handler wants a number instead.
    target    TEXT,
    amount    INTEGER NOT NULL DEFAULT 0,
    ordinal   INTEGER NOT NULL DEFAULT 0,
    CHECK ((kind = 'points' AND amount > 0) OR (kind <> 'points' AND target IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS reward_payouts_reward_idx ON reward_payouts (reward_id, ordinal);

-- ── Baselines: why a new reward does not pay for history ────────────────────
-- Recorded when a per_unit reward is created. Without it, a reward worth a
-- point per grab pays every grab the site ever recorded, for everyone, on its
-- first run.
CREATE TABLE IF NOT EXISTS reward_baselines (
    reward_id BIGINT NOT NULL REFERENCES rewards(id) ON DELETE CASCADE,
    user_id   BIGINT NOT NULL,
    value     BIGINT NOT NULL,
    PRIMARY KEY (reward_id, user_id)
);

-- ── Grants: what is owed and what was paid ──────────────────────────────────
CREATE TABLE IF NOT EXISTS reward_grants (
    id        BIGSERIAL PRIMARY KEY,
    reward_id BIGINT NOT NULL REFERENCES rewards(id),
    user_id   BIGINT NOT NULL,
    -- Meaning depends on rewards.kind: 0, an event_windows.id, or a high-water
    -- mark. One number per grant, because one grant covers every payout line.
    reference BIGINT NOT NULL,
    state     TEXT NOT NULL DEFAULT 'pending'
              CHECK (state IN ('pending','credited','expired')),
    issuance_id BIGINT,
    reason     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Copied from the reward at grant time, so extending a reward's expiry
    -- does not retroactively revive grants that already lapsed.
    expires_at TIMESTAMPTZ,
    settled_at TIMESTAMPTZ,
    -- THE line that makes the model work. "Do not pay twice" stops being
    -- application logic in three places and becomes a constraint: a buggy
    -- reward proposing a duplicate gets a violation, not a second payment.
    UNIQUE (reward_id, user_id, reference)
);
CREATE INDEX IF NOT EXISTS reward_grants_pending_idx
    ON reward_grants (user_id, state) WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS reward_grants_expiring_idx
    ON reward_grants (expires_at) WHERE state = 'pending' AND expires_at IS NOT NULL;

-- What a grant handed over, FROZEN at grant time. A member offered 50 points
-- and a medal must receive exactly that even if an admin retunes the reward
-- before they claim: what was offered is what is owed.
CREATE TABLE IF NOT EXISTS reward_grant_payouts (
    id       BIGSERIAL PRIMARY KEY,
    grant_id BIGINT NOT NULL REFERENCES reward_grants(id) ON DELETE CASCADE,
    kind     TEXT   NOT NULL CHECK (kind IN ('points','role','medal','achievement','username_fx')),
    target   TEXT,
    amount   INTEGER NOT NULL DEFAULT 0,
    -- Set once the line is executed, so a partially-delivered grant (points
    -- credited, medal handler down) resumes instead of replaying.
    settled_at TIMESTAMPTZ,
    CHECK ((kind = 'points' AND amount > 0) OR (kind <> 'points' AND target IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS reward_grant_payouts_grant_idx ON reward_grant_payouts (grant_id);

-- ── Issuances: deliberate retroactive grants ────────────────────────────────
CREATE TABLE IF NOT EXISTS reward_issuances (
    id        BIGSERIAL PRIMARY KEY,
    reward_id BIGINT NOT NULL REFERENCES rewards(id),
    -- Named cohorts, never stored SQL: an arbitrary query in a table is an
    -- injection surface and unmaintainable, and the set anyone wants is small.
    cohort     TEXT NOT NULL CHECK (cohort IN ('all','active_since','registered_before','explicit')),
    cohort_arg TEXT,
    reason     TEXT NOT NULL,
    issued_by  BIGINT,
    issued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted    INTEGER NOT NULL DEFAULT 0
);
