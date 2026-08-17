-- Migration 002 — Achievements.
--
-- SUPERSEDED: these tables' successor lives in the ACHIEVEMENTS plugin
-- (achievements/migrations/001_init.sql), which lifts their rows on its first
-- boot and records payment as its own paid_at fact instead of a grant link.
-- This file stays because migration history is append-only, and the tables
-- stay until the operator drops them once satisfied the lift is complete —
-- nothing writes them any more. The comments below describe the design as it
-- was when this was the achievements' home.
--
-- An achievement is a CRITERION attached to a reward. That split is the whole
-- design: the rewards engine already owns definitions, repeatability,
-- triggers, jobs, callbacks and pay-once-as-a-constraint, and the one thing it
-- cannot express is "reach N of X". per_unit counts a number and pays per
-- delta; it has no threshold that latches.
--
-- So this migration adds the criterion and the per-member progress, and
-- delegates everything about PAYING to the reward named by reward_id.
--
-- Notably absent: a `repeatable` column. rewards.kind already declares
-- repeatability and the engine enforces it through the reference it computes
-- for each grant. A second copy here could disagree — an achievement marked
-- repeatable whose reward is one_off would complete over and over and pay
-- exactly once, with nothing reporting it. Validation restricts achievements
-- to one_off rewards (see validate.go); allowing recurring later needs no
-- schema change, only a validation change.

CREATE TABLE IF NOT EXISTS achievements (
    id          BIGSERIAL PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',

    -- What it pays. RESTRICT rather than CASCADE: deleting the reward out
    -- from under earned achievements would orphan grants members already
    -- hold, which is the same reason rewards.event_id is RESTRICT.
    reward_id   BIGINT NOT NULL REFERENCES rewards(id) ON DELETE RESTRICT,

    -- The criterion. metric names a counter a host registers; threshold is
    -- how much of it. A metric with no registered source is INERT, never an
    -- error — the same rule the engine applies to a payout kind with no
    -- handler, so a half-configured site degrades rather than failing to boot.
    metric      TEXT   NOT NULL,
    threshold   BIGINT NOT NULL CHECK (threshold > 0),

    -- Which surface's activity can move this. A page fires one trigger and
    -- only the achievements carrying it are evaluated, so an upload does not
    -- re-check every comment achievement on the site.
    trigger     TEXT NOT NULL DEFAULT '',

    -- Ordering is the only presentation this table carries. Icons and colours
    -- are the HOST's: one stored here is one the site cannot override and the
    -- admin UI cannot set.
    ordinal     INTEGER NOT NULL DEFAULT 0,
    -- Secret achievements: withheld from the catalogue until earned.
    hidden      BOOLEAN NOT NULL DEFAULT FALSE,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS achievements_trigger_idx ON achievements (trigger) WHERE enabled;
CREATE INDEX IF NOT EXISTS achievements_metric_idx  ON achievements (metric)  WHERE enabled;

-- ── Per-member standing ─────────────────────────────────────────────────────
-- One row per (achievement, member), created lazily on first progress.
CREATE TABLE IF NOT EXISTS user_achievements (
    achievement_id BIGINT NOT NULL REFERENCES achievements(id) ON DELETE CASCADE,
    user_id        BIGINT NOT NULL,

    -- Where they are against the threshold. Kept even after completion so a
    -- page can say "100 / 100" rather than going blank at the finish line.
    progress       BIGINT  NOT NULL DEFAULT 0,
    -- Completions. Always 0 or 1 while achievements are restricted to one_off
    -- rewards; a column now rather than a migration later, because a
    -- recurring achievement genuinely has several and the read wants to say so.
    times          INTEGER NOT NULL DEFAULT 0,
    completed_at   TIMESTAMPTZ,

    -- The grant this completion produced, written in the SAME transaction as
    -- the completion. Neither is allowed to happen alone: a completion with no
    -- grant is an achievement that paid nothing, and a grant with no
    -- completion pays again on the next evaluation because nothing records
    -- that it already fired.
    --
    -- No CASCADE: grants are never deleted (the engine expires them in place),
    -- and if one ever were, losing the completion with it would silently make
    -- the achievement earnable again.
    grant_id       BIGINT REFERENCES reward_grants(id),

    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (achievement_id, user_id),

    -- A completion must carry its grant, and a grant must carry its
    -- completion. This is the invariant the transaction exists to hold; as a
    -- constraint it also catches any future writer that tries to set one
    -- without the other.
    CHECK ((completed_at IS NULL) = (grant_id IS NULL))
);

-- The member's own page reads every row for one user; the job reads by
-- achievement. Both are covered: the PK serves the second, this the first.
CREATE INDEX IF NOT EXISTS user_achievements_user_idx ON user_achievements (user_id);
