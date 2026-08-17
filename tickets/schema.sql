-- Schema the tickets plugin expects the HOST to provide.
--
-- The plugin owns no tables. On this site they arrived as host migration 41
-- (with `public` added later by 207) in the public schema, and stayed there
-- through the extraction: moving live tables into a plugin schema is a data
-- migration, and the port moved CODE. This file exists so a host that does NOT
-- already have them can create them in one step, rather than reading the store
-- to work out what they must look like.
--
-- Every statement is idempotent. Adjust the users(id) references to whatever
-- the host's user table is called.

CREATE TABLE IF NOT EXISTS support_tickets (
    id          BIGSERIAL PRIMARY KEY,
    user_id     INTEGER     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    username    TEXT        NOT NULL,
    subject     TEXT        NOT NULL,
    body        TEXT        NOT NULL,
    priority    TEXT        NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high')),
    status      TEXT        NOT NULL DEFAULT 'open'   CHECK (status   IN ('open','in_progress','closed')),
    admin_note  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- (Host migration 207 once added an opt-in `public` BOOLEAN here; the feature
-- was retired 2026-08-16. Hosts that ran 207 keep the column, dead — the
-- plugin no longer reads it. Fresh installs never get it.)

-- Host migration 44.
CREATE TABLE IF NOT EXISTS ticket_replies (
    id         BIGSERIAL PRIMARY KEY,
    ticket_id  BIGINT NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    username   TEXT NOT NULL,
    body       TEXT NOT NULL,
    is_admin   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_support_tickets_created ON support_tickets(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_support_tickets_status ON support_tickets(status);
CREATE INDEX IF NOT EXISTS idx_support_tickets_updated ON support_tickets(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_support_tickets_user_id ON support_tickets(user_id);
CREATE INDEX IF NOT EXISTS idx_ticket_replies_ticket ON ticket_replies(ticket_id, created_at);
