-- Scheduled events leave this plugin, and `reference` stops meaning three things.
--
-- Events moved to the `events` plugin (its own schema, its own generator, its own
-- admin page), because a season or a reset period is a site fact several systems
-- reference -- news wants to publish when one opens, a leaderboard wants to reset
-- on one -- and none of them should reach into rewards to ask. The comment on the
-- table this drops said as much: "not reward-specific in meaning even though it
-- lives here for now."
--
-- SAFE ONLY BECAUSE EVERYTHING IS EMPTY, verified against production before
-- writing this: 0 events, 0 event_windows, 0 rewards with an event_id, 0
-- recurring rewards, 0 per_unit grants, and all 26 existing grants at
-- reference = 0. The guards below make that a checked precondition rather than a
-- remembered one -- on any host where it is false this migration REFUSES rather
-- than silently discarding payment history.

-- ── rewards.event_id → scheduled_event_slug ─────────────────────────────────
--
-- A slug, not an id. Ids belong to the schema that owns them, so keeping a
-- BIGINT here would couple rewards to another plugin's table and would break the
-- moment that table was rebuilt or restored from another host's dump. A slug is
-- also what an operator picks from a dropdown.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM rewards WHERE event_id IS NOT NULL) THEN
        RAISE EXCEPTION
            'rewards.event_id has rows; migration 005 would drop the link to events that are moving schema. Map them to scheduled_event_slug by hand first.';
    END IF;
END $$;

ALTER TABLE rewards ADD COLUMN IF NOT EXISTS scheduled_event_slug TEXT;
ALTER TABLE rewards DROP COLUMN IF EXISTS event_id;

-- ── reference splits in two ─────────────────────────────────────────────────
--
-- It carried three unrelated meanings depending on rewards.kind: 0 for one_off,
-- an event_windows.id for recurring, and a numerically-compared high-water mark
-- for per_unit. One column doing two jobs is why it could not simply become TEXT
-- -- a per_unit mark compared as text makes '9' greater than '10'.
--
--   reference  TEXT   — WHICH entitlement this grant is for. The occurrence key
--                       ("summer-2026@2026-08-01T00:00:00Z") for recurring,
--                       empty for one_off. Keeps the pay-once UNIQUE.
--   high_water BIGINT — HOW FAR we have paid. per_unit only.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM reward_grants WHERE reference <> 0) THEN
        RAISE EXCEPTION
            'reward_grants.reference has non-zero rows; migration 005 cannot translate a window id or a high-water mark automatically. Split them by hand first.';
    END IF;
END $$;

ALTER TABLE reward_grants ADD COLUMN IF NOT EXISTS high_water BIGINT NOT NULL DEFAULT 0;

-- The UNIQUE has to go before the column type can change, and come back after.
-- Named explicitly rather than left to Postgres's generated name, because the
-- generated one differs between the CREATE TABLE form and this recreation and a
-- later migration referring to it would miss.
ALTER TABLE reward_grants DROP CONSTRAINT IF EXISTS reward_grants_reward_id_user_id_reference_key;

-- Every surviving row is reference = 0 (guarded above), which becomes the empty
-- string: a one_off grant's entitlement has no name because there is only one.
ALTER TABLE reward_grants
    ALTER COLUMN reference TYPE TEXT USING CASE WHEN reference = 0 THEN '' ELSE reference::TEXT END,
    ALTER COLUMN reference SET DEFAULT '';

-- THE line that makes the model work, restored. "Do not pay twice" stays a
-- constraint rather than application logic in three places: a buggy reward
-- proposing a duplicate gets a violation, not a second payment.
ALTER TABLE reward_grants DROP CONSTRAINT IF EXISTS reward_grants_pay_once;
ALTER TABLE reward_grants ADD CONSTRAINT reward_grants_pay_once
    UNIQUE (reward_id, user_id, reference);

-- ── The tables themselves ───────────────────────────────────────────────────
--
-- Last, after the guards above have had their say. CASCADE on event_windows is
-- its own FK to events, not a reach into anything else.
DROP TABLE IF EXISTS event_windows;
DROP TABLE IF EXISTS events;
