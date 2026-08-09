-- perks plugin schema. Applied by loon into the "perks" schema.
--
-- Tokens: a member buys one with points and later spends it on a specific
-- torrent. Two rows' worth of state in one table, because an unspent token and
-- a spent one are the same object at different times, and splitting them would
-- mean moving a row every time somebody used one.

CREATE TABLE IF NOT EXISTS tokens (
    id          BIGSERIAL   PRIMARY KEY,
    user_id     BIGINT      NOT NULL,
    -- 'freeleech' or 'upload2x'. Text rather than an enum so a site can add a
    -- kind without a migration; unknown kinds are ignored by the multiplier
    -- rather than treated as an error, which is the safe direction — an
    -- unrecognised perk should do nothing, not everything.
    kind        TEXT        NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Set when spent. NULL means the member is still holding it.
    info_hash   CHAR(40)    NULL,
    spent_at    TIMESTAMPTZ NULL,
    -- When the effect stops. Set at SPEND time from the then-current setting,
    -- so changing the site's token duration never shortens a perk somebody has
    -- already paid for and started using.
    expires_at  TIMESTAMPTZ NULL
);

-- The multiplier's lookup: every active perk, to load into memory. Partial,
-- because unspent and expired tokens are the majority and neither affects an
-- announce.
CREATE INDEX IF NOT EXISTS tokens_active_idx
    ON tokens (user_id, info_hash)
    WHERE spent_at IS NOT NULL;

-- A member's wallet: what they are holding, newest first.
CREATE INDEX IF NOT EXISTS tokens_wallet_idx
    ON tokens (user_id, acquired_at DESC);

-- One token per member per torrent per kind. Spending a second freeleech token
-- on a torrent that already has one buys nothing, and letting it happen would
-- take points for no effect.
CREATE UNIQUE INDEX IF NOT EXISTS tokens_one_per_torrent_idx
    ON tokens (user_id, info_hash, kind)
    WHERE info_hash IS NOT NULL;
