-- store plugin schema — points-funded item catalog + purchase ledger.
-- Applied by loon's plugin-migration runner (core.RunPluginMigrations)
-- with search_path scoped to the "store" schema, so the unqualified
-- names below become store.items / store.purchases. Append-only +
-- idempotent, same rules as the host-numbered series.

CREATE TABLE IF NOT EXISTS items (
    id           SERIAL PRIMARY KEY,
    name         TEXT        NOT NULL,
    description  TEXT        NOT NULL DEFAULT '',
    points_cost  INT         NOT NULL,
    -- reward_type is the capability the purchase invokes ('rank' for
    -- now; 'points_bonus'/'freeleech'/'invites' as more granters land).
    reward_type  TEXT        NOT NULL,
    -- reward_ref is the type-specific target — the rank id for
    -- reward_type='rank'. reward_days is the grant duration (0 = n/a).
    reward_ref   TEXT        NOT NULL DEFAULT '',
    reward_days  INT         NOT NULL DEFAULT 0,
    -- stock is the remaining count; -1 means unlimited.
    stock        INT         NOT NULL DEFAULT -1,
    active       BOOLEAN     NOT NULL DEFAULT TRUE,
    sort_order   INT         NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purchases (
    id           SERIAL PRIMARY KEY,
    user_id      INT         NOT NULL,
    item_id      INT         NOT NULL,
    points_spent INT         NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_purchases_user ON purchases (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_items_active ON items (sort_order) WHERE active;
