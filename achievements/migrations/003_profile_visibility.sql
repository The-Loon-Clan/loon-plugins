-- Whether a member's badges appear on somebody ELSE's view of their profile.
--
-- Plugin-side, not a host user_preferences column, because it is an
-- achievements fact: the host has no reason to carry a column about a plugin's
-- card, and a second host mounting this plugin would otherwise have to add one
-- before the opt-out worked at all. The plugin owns the card, the rule, the
-- storage and the page that sets it.
--
-- ABSENCE IS SHOWN. Earned badges are public by design — that is what makes
-- them worth earning — so the default is the absent row and the default of the
-- column, and only a member who has actually said "hide" has a row saying so.
-- A read that finds nothing is therefore the same answer as a read of a fresh
-- database, which is the property that makes this safe to consult from a
-- render path.
CREATE TABLE IF NOT EXISTS profile_visibility (
    -- No FK: users live in the host's schema, and a plugin that references it
    -- cannot be uninstalled. Same convention as user_achievements.user_id.
    user_id    BIGINT      PRIMARY KEY,
    hidden     BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
