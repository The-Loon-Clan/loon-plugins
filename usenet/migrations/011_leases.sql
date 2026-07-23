-- Cross-host coordination, so the crawler can run on more than one server.
--
-- Two different problems, one mechanism:
--
--   scope='backbone'  which worker owns crawling a given backbone. Backbones
--                     share no mutable state (see PROVIDERS.md), so workers
--                     holding different backbones run fully in parallel. This
--                     is what makes horizontal ingest possible at all — and it
--                     also fixes the connection budget structurally, since a
--                     provider account is then used by exactly one worker
--                     rather than N of them all opening the full allowance.
--
--   scope='job'       jobs that are NOT backbone-scoped (NZB build, prune, tag
--                     fill, health). Those must still run once cluster-wide, or
--                     two workers duplicate the work and, for health, compete
--                     for the same idle connections.
--
-- A LEASE rather than an advisory lock, deliberately. Postgres advisory locks
-- are session-scoped, so holding one for the length of a crawl means pinning a
-- connection idle-in-transaction for minutes. A lease row with an expiry needs
-- no pinned connection, survives a worker being killed (the lease simply times
-- out and someone else claims it), and is the same primitive for both scopes.
--
-- Claiming is a single atomic upsert guarded on "expired, or already mine", so
-- two workers racing for the same key cannot both win.
CREATE TABLE IF NOT EXISTS leases (
    scope      TEXT        NOT NULL,   -- 'backbone' | 'job'
    key        TEXT        NOT NULL,
    worker_id  TEXT        NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, key)
);

-- Reclaiming expired leases is the hot path when a worker dies.
CREATE INDEX IF NOT EXISTS idx_leases_expiry ON leases (expires_at);
