-- achievements plugin schema. Applied by loon into the "achievements" schema
-- with search_path scoped (achievements, public), so unqualified names resolve
-- here. Idempotent.
--
-- SUCCESSION, not invention: these tables succeed rewards.achievements and
-- rewards.user_achievements, which stay behind (see the data lift at the
-- bottom). The design changes from the old home:
--
--   * THE REWARD IS OPTIONAL. reward_slug names a one_off reward in the
--     rewards plugin, paid through pluginapi.RewardBySlugGranter; '' means a
--     pure badge, which is now a legitimate achievement. The old reward_id FK
--     into rewards.rewards is exactly the cross-schema coupling this plugin
--     exists to remove, and a slug survives the rewards table being rebuilt
--     where an id would not.
--
--   * NO grant_id, and NO CHECK tying completion to a grant. The old
--     CHECK ((completed_at IS NULL) = (grant_id IS NULL)) enforced the
--     one-transaction design; across plugins that transaction cannot exist,
--     so completion is this plugin's own atomic fact and payment is recorded
--     separately in paid_at, repaired by the scoring job when the two get
--     separated by a crash (the granter is idempotent).
--
--   * A TRIGGER is now a first-class criterion: an achievement either counts
--     a metric to a threshold, or completes the moment a declared event
--     fires. The CHECK below carries the either/or rule.

CREATE TABLE IF NOT EXISTS achievements (
    id          BIGSERIAL PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',

    -- What it pays, if anything. A slug into the REWARDS plugin's catalogue,
    -- deliberately not a foreign key (another schema, another plugin); ''
    -- means a pure badge. An unpayable slug is not detectable here and is
    -- reported lazily by the scoring job.
    reward_slug TEXT NOT NULL DEFAULT '',

    -- The criterion, one shape or the other:
    --   metric + threshold  -- reach N of a counter (a host MetricSource or a
    --                          countable declared event);
    --   trigger             -- complete once when this declared event fires.
    metric      TEXT   NOT NULL DEFAULT '',
    threshold   BIGINT NOT NULL DEFAULT 0,
    trigger     TEXT   NOT NULL DEFAULT '',
    CONSTRAINT achievements_criterion_check
        CHECK ((metric <> '' AND threshold > 0) OR trigger <> ''),

    -- The look, chosen by the operator who defines it. icon is a host sprite
    -- symbol name; image_path an uploaded image's public URL, which wins when
    -- set. Both default to '', meaning "the host decides".
    icon        TEXT NOT NULL DEFAULT '',
    image_path  TEXT NOT NULL DEFAULT '',

    ordinal     INTEGER NOT NULL DEFAULT 0,
    -- Secret achievements: withheld from the catalogue until earned.
    hidden      BOOLEAN NOT NULL DEFAULT FALSE,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,

    -- When the first scoring pass finished. NULL means it has never been
    -- scored, so the next pass is the backfill and its completions are
    -- silent. A timestamp rather than a boolean because "when did we backfill
    -- this" is the question an operator asks when a member says they were
    -- never told.
    backfilled_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS achievements_trigger_idx ON achievements (trigger) WHERE enabled;
CREATE INDEX IF NOT EXISTS achievements_metric_idx  ON achievements (metric)  WHERE enabled;

-- ── Per-member standing ─────────────────────────────────────────────────────
-- One row per (achievement, member), created lazily on first progress -- or on
-- completion itself, for a trigger achievement whose first contact with the
-- member is the event firing.
--
-- No foreign key to the host's users table (user_id is a plain BIGINT); the
-- host drives cleanup on member deletion, the same rule every plugin here
-- follows.
CREATE TABLE IF NOT EXISTS user_achievements (
    achievement_id BIGINT NOT NULL REFERENCES achievements(id) ON DELETE CASCADE,
    user_id        BIGINT NOT NULL,

    -- Where they are against the threshold. Kept even after completion so a
    -- page can say "100 / 100" rather than going blank at the finish line.
    progress       BIGINT  NOT NULL DEFAULT 0,
    -- Completions. Always 0 or 1 while completion latches once; a column now
    -- rather than a migration later, because a recurring achievement
    -- genuinely has several and the read wants to say so.
    times          INTEGER NOT NULL DEFAULT 0,
    completed_at   TIMESTAMPTZ,

    -- When the payment half settled: the reward grant landed (or nothing was
    -- owed -- a pure badge, or a direct AchievementGranter award). NULL on a
    -- completed row means the reward is still owed, and the scoring job's
    -- repair sweep pays it. This column replaces the old grant_id + CHECK:
    -- payment is repairable state now, not half of an invariant.
    paid_at        TIMESTAMPTZ,

    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (achievement_id, user_id)
);

-- The member's own page reads every row for one user; the job reads by
-- achievement. Both are covered: the PK serves the second, this the first.
CREATE INDEX IF NOT EXISTS user_achievements_user_idx ON user_achievements (user_id);
-- The repair sweep's read: completed rows whose payment never landed. Partial,
-- because in a healthy system this index is nearly empty.
CREATE INDEX IF NOT EXISTS user_achievements_unpaid_idx
    ON user_achievements (achievement_id, user_id)
    WHERE completed_at IS NOT NULL AND paid_at IS NULL;

-- ── Data lift from the old home ─────────────────────────────────────────────
--
-- A ONE-TIME SUCCESSION, and the one place this plugin reads another schema.
-- Cross-schema reads are normally forbidden here for the same reason foreign
-- keys to host tables are -- but a plugin extracted from another one has to
-- carry its data across exactly once, and doing it in the migration (guarded,
-- idempotent) beats an operator hand-running INSERT..SELECTs. Verified
-- against the reference deployment before writing: 5 rows in
-- rewards.achievements, 12 in rewards.user_achievements (6 completed), and 0
-- completed rows without a grant_id -- the old CHECK made completion-with-
-- grant unconditional -- so completed => paid holds for the whole population
-- being lifted.
--
-- Guarded by to_regclass so a FRESH install -- no rewards schema, or a
-- rewards schema that never had achievements -- skips it silently. Qualified
-- names (rewards.achievements) because search_path deliberately does not
-- include rewards. ON CONFLICT DO NOTHING makes a re-run (or a restored dump
-- racing a boot) a no-op.
--
-- The OLD tables are NOT dropped. They stop being written the moment the
-- rewards plugin ships without its achievements half, but dropping data is
-- the operator's call, made once they are satisfied the lift is complete:
--
--     DROP TABLE rewards.user_achievements; DROP TABLE rewards.achievements;

DO $$
BEGIN
    IF to_regclass('rewards.achievements') IS NOT NULL
       AND to_regclass('rewards.rewards') IS NOT NULL THEN
        -- reward_id -> reward_slug via the join; a reward deleted out from
        -- under an old achievement maps to '' (a pure badge), which is the
        -- honest translation of "pointed at nothing".
        INSERT INTO achievements
            (slug, name, description, reward_slug, metric, threshold, trigger,
             icon, image_path, ordinal, hidden, enabled, backfilled_at, created_at)
        SELECT a.slug, a.name, a.description, COALESCE(r.slug, ''),
               a.metric, a.threshold, a.trigger,
               a.icon, a.image_path, a.ordinal, a.hidden, a.enabled,
               a.backfilled_at, a.created_at
          FROM rewards.achievements a
          LEFT JOIN rewards.rewards r ON r.id = a.reward_id
        ON CONFLICT (slug) DO NOTHING;
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('rewards.user_achievements') IS NOT NULL
       AND to_regclass('rewards.achievements') IS NOT NULL THEN
        -- Rows are re-keyed through the slug because the new ids are freshly
        -- assigned. Completed rows with a grant_id were paid under the old
        -- one-transaction design, so paid_at = completed_at; the old CHECK
        -- guaranteed no completed row lacks a grant, so no lifted completion
        -- lands on the repair sweep's list.
        INSERT INTO user_achievements
            (achievement_id, user_id, progress, times, completed_at, paid_at, updated_at)
        SELECT na.id, ua.user_id, ua.progress, ua.times, ua.completed_at,
               CASE WHEN ua.grant_id IS NOT NULL THEN ua.completed_at END,
               ua.updated_at
          FROM rewards.user_achievements ua
          JOIN rewards.achievements oa ON oa.id = ua.achievement_id
          JOIN achievements na ON na.slug = oa.slug
        ON CONFLICT (achievement_id, user_id) DO NOTHING;
    END IF;
END $$;
